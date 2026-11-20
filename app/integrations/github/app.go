// Package github es el adapter de la GitHub App de Tekitla (issue 016): la
// API (con la llave privada de la App, que nunca sale de acá) y los webhooks
// (firma HMAC y traducción a model.RepoEvent).
//
// Seguridad (plan §8 del issue):
//   - Solo habla con APIBase (https://api.github.com en todos los ambientes;
//     otra base solo en tests), sin seguir redirecciones, con timeout de 10 s
//     y un tope de 5 MiB por respuesta.
//   - Los tokens de instalación (1 h) viven en memoria; nunca en la base ni
//     en logs.
package github

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// DefaultAPIBase es la API de GitHub.
const DefaultAPIBase = "https://api.github.com"

const (
	maxResponseBytes = 5 << 20
	requestTimeout   = 10 * time.Second
	// maxRepoPages * 100 repos se leen de la instalación; el tope de 300 por
	// proyecto se aplica después del alcance (model.SelectRepos), así un repo
	// elegido en la página 4 no se pierde.
	maxRepoPages = 30
)

// Config es la configuración de la App. Sale de variables de entorno
// (GITHUB_APP_*), que carga Victor en Coolify.
type Config struct {
	AppID int64
	// Slug de la App (tekitla-qa, tekitla), para la URL de instalación.
	Slug string
	// PrivateKeyPEM es el PEM de la llave privada (ya decodificado del
	// base64 de la variable).
	PrivateKeyPEM []byte
	WebhookSecret []byte
	// APIBase: vacío = DefaultAPIBase. Otra solo en tests.
	APIBase string
}

// App implementa port.GitHubApp.
type App struct {
	appID   int64
	slug    string
	key     *rsa.PrivateKey
	secret  []byte
	apiBase string
	http    *http.Client
	now     func() time.Time

	mu     sync.Mutex
	tokens map[int64]installationToken
}

type installationToken struct {
	token   string
	expires time.Time
}

var _ port.GitHubApp = (*App)(nil)

// New valida la configuración y construye la App.
func New(cfg Config) (*App, error) {
	if cfg.AppID <= 0 {
		return nil, errors.New("github app: GITHUB_APP_ID must be a positive number")
	}
	if !slugOK(cfg.Slug) {
		return nil, errors.New("github app: GITHUB_APP_SLUG is invalid")
	}
	if len(cfg.WebhookSecret) < 16 {
		return nil, errors.New("github app: GITHUB_WEBHOOK_SECRET must have at least 16 characters")
	}
	key, err := parsePrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	base := cfg.APIBase
	if base == "" {
		base = DefaultAPIBase
	}
	return &App{
		appID:   cfg.AppID,
		slug:    cfg.Slug,
		key:     key,
		secret:  cfg.WebhookSecret,
		apiBase: strings.TrimRight(base, "/"),
		http: &http.Client{
			Timeout: requestTimeout,
			// Nunca seguir redirecciones: solo se habla con apiBase.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now:    time.Now,
		tokens: map[int64]installationToken{},
	}, nil
}

// DecodePrivateKey decodifica el valor de GITHUB_APP_PRIVATE_KEY: el PEM en
// base64 (una línea, cómodo en Coolify) o el PEM tal cual.
func DecodePrivateKey(v string) ([]byte, error) {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "-----BEGIN") {
		return []byte(v), nil
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, errors.New("github app: GITHUB_APP_PRIVATE_KEY is not base64")
	}
	return b, nil
}

func slugOK(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("github app: private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("github app: private key is not an RSA key")
	}
	k, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github app: private key is not an RSA key")
	}
	return k, nil
}

// WithClock reemplaza el reloj (tests).
func (a *App) WithClock(now func() time.Time) *App {
	a.now = now
	return a
}

// InstallURL es la página de instalación de la App.
func (a *App) InstallURL() string {
	return "https://github.com/apps/" + a.slug + "/installations/new"
}

// appJWT firma el JWT de la App (RS256, 9 minutos; iat un minuto atrás por
// diferencias de reloj, como pide GitHub).
func (a *App) appJWT() (string, error) {
	now := a.now().Unix()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now - 60, "exp": now + 9*60, "iss": strconv.FormatInt(a.appID, 10)})
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// installationToken devuelve un token de la instalación (de la caché si le
// quedan más de 5 minutos).
func (a *App) installationToken(ctx context.Context, installationID int64) (string, error) {
	a.mu.Lock()
	t, ok := a.tokens[installationID]
	a.mu.Unlock()
	if ok && a.now().Add(5*time.Minute).Before(t.expires) {
		return t.token, nil
	}
	jwt, err := a.appJWT()
	if err != nil {
		return "", domainerrors.InternalError("could not sign GitHub App JWT", nil)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := "/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	if err := a.do(ctx, http.MethodPost, path, jwt, &out); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.tokens[installationID] = installationToken{token: out.Token, expires: out.ExpiresAt}
	a.mu.Unlock()
	return out.Token, nil
}

// Installation devuelve la cuenta de la instalación.
func (a *App) Installation(ctx context.Context, installationID int64) (string, error) {
	jwt, err := a.appJWT()
	if err != nil {
		return "", domainerrors.InternalError("could not sign GitHub App JWT", nil)
	}
	var out struct {
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := a.do(ctx, http.MethodGet, "/app/installations/"+strconv.FormatInt(installationID, 10), jwt, &out); err != nil {
		return "", err
	}
	if !ownerOK(out.Account.Login) {
		return "", domainerrors.ServiceUnavailableError("unexpected GitHub response", nil)
	}
	return out.Account.Login, nil
}

type apiRepo struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	DefaultBranch string   `json:"default_branch"`
	Topics        []string `json:"topics"`
	Archived      bool     `json:"archived"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// ListRepos devuelve los repos de la instalación (hasta 300).
func (a *App) ListRepos(ctx context.Context, installationID int64) ([]model.RemoteRepo, error) {
	token, err := a.installationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	var out []model.RemoteRepo
	for page := 1; page <= maxRepoPages; page++ {
		var resp struct {
			Repositories []apiRepo `json:"repositories"`
		}
		path := "/installation/repositories?per_page=100&page=" + strconv.Itoa(page)
		if err := a.do(ctx, http.MethodGet, path, "token "+token, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Repositories {
			if r.ID <= 0 || !ownerOK(r.Owner.Login) || !repoNameOK(r.Name) {
				continue
			}
			out = append(out, model.RemoteRepo{
				ExternalID: r.ID, Owner: r.Owner.Login, Name: r.Name,
				DefaultBranch: model.Truncate(r.DefaultBranch, model.BranchMaxLen),
				Topics:        r.Topics, Archived: r.Archived,
			})
		}
		if len(resp.Repositories) < 100 {
			break
		}
	}
	return out, nil
}

// ListOpenPRs devuelve los PR abiertos de un repo (hasta 100) como eventos.
func (a *App) ListOpenPRs(ctx context.Context, installationID int64, owner, name string) ([]model.RepoEvent, error) {
	token, err := a.installationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	var prs []apiPR
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/pulls?state=open&per_page=100"
	if err := a.do(ctx, http.MethodGet, path, "token "+token, &prs); err != nil {
		return nil, err
	}
	out := make([]model.RepoEvent, 0, len(prs))
	for i := range prs {
		if ev, ok := prs[i].event(prs[i].User.Login); ok {
			out = append(out, ev)
		}
	}
	return out, nil
}

// PullRequest devuelve un PR (en cualquier estado) como evento.
func (a *App) PullRequest(ctx context.Context, installationID int64, owner, name string, number int) (model.RepoEvent, error) {
	token, err := a.installationToken(ctx, installationID)
	if err != nil {
		return model.RepoEvent{}, err
	}
	var pr apiPR
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/pulls/" + strconv.Itoa(number)
	if err := a.do(ctx, http.MethodGet, path, "token "+token, &pr); err != nil {
		return model.RepoEvent{}, err
	}
	ev, ok := pr.event(pr.User.Login)
	if !ok {
		return model.RepoEvent{}, domainerrors.NotFoundError("pull request without shape", nil)
	}
	return ev, nil
}

// do hace un request a la API y decodifica la respuesta. 404 → NotFound;
// cualquier otra falla → Unavailable (sin el cuerpo de GitHub).
func (a *App) do(ctx context.Context, method, path, auth string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, a.apiBase+path, nil)
	if err != nil {
		return domainerrors.InternalError("could not build GitHub request", nil)
	}
	if strings.HasPrefix(auth, "token ") {
		req.Header.Set("Authorization", auth)
	} else {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "tekitla")
	resp, err := a.http.Do(req)
	if err != nil {
		return domainerrors.ServiceUnavailableError("GitHub is not reachable", nil)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return domainerrors.ServiceUnavailableError("GitHub response too large or unreadable", nil)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return domainerrors.NotFoundError("not found in GitHub", nil)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return domainerrors.ServiceUnavailableError("GitHub answered "+strconv.Itoa(resp.StatusCode), nil)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return domainerrors.ServiceUnavailableError("unexpected GitHub response", nil)
	}
	return nil
}

// VerifyWebhook comprueba X-Hub-Signature-256 ("sha256=<hex>") en tiempo
// constante.
func (a *App) VerifyWebhook(body []byte, signature string) bool {
	return VerifySignature(a.secret, body, signature)
}

// VerifySignature es VerifyWebhook con el secreto explícito.
func VerifySignature(secret, body []byte, signature string) bool {
	hexSig, ok := strings.CutPrefix(signature, "sha256=")
	if !ok || len(hexSig) != sha256.Size*2 {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// Sign devuelve la firma de body (tests y herramientas locales).
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func ownerOK(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func repoNameOK(s string) bool { return ownerOK(s) }

// errBadPayload es un cuerpo que no se puede leer.
func errBadPayload(what string) error {
	return domainerrors.ValidationError(fmt.Sprintf("invalid webhook payload: %s", what), nil)
}

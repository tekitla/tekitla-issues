package github

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Payloads inventados (repo acme/api, logins dev-prueba): nunca datos reales.

const (
	sha1 = "1111111111111111111111111111111111111111"
	sha2 = "2222222222222222222222222222222222222222"
)

func TestVerifySignature(t *testing.T) {
	secret := []byte("secreto-de-prueba-0123456789")
	body := []byte(`{"zen":"hola"}`)
	sig := Sign(secret, body)
	if !VerifySignature(secret, body, sig) {
		t.Fatal("firma válida rechazada")
	}
	for name, s := range map[string]string{
		"vacía":        "",
		"sin prefijo":  strings.TrimPrefix(sig, "sha256="),
		"sha1":         "sha1=" + strings.TrimPrefix(sig, "sha256="),
		"otro secreto": Sign([]byte("otro-secreto-0123456789"), body),
		"no hex":       "sha256=" + strings.Repeat("z", 64),
		"corta":        sig[:20],
	} {
		if VerifySignature(secret, body, s) {
			t.Errorf("%s: aceptada", name)
		}
	}
	if VerifySignature(secret, []byte(`{"zen":"chau"}`), sig) {
		t.Error("cuerpo cambiado: aceptada")
	}
}

func TestParsePush(t *testing.T) {
	body := `{"ref":"refs/heads/feat/tk-9-login","after":"` + sha1 + `","deleted":false,
	  "repository":{"id":42},"sender":{"login":"dev-prueba"},
	  "commits":[{"id":"a"},{"id":"` + sha1 + `"}],
	  "head_commit":{"id":"` + sha1 + `","message":"feat: login con Google\n\ndetalle"}}`
	repoID, evs, err := ParseWebhook("push", []byte(body))
	if err != nil || repoID != 42 || len(evs) != 1 {
		t.Fatalf("got %d %v %v", repoID, evs, err)
	}
	ev := evs[0]
	if ev.Kind != model.RepoEventPush || ev.Branch != "feat/tk-9-login" || ev.HeadSHA != sha1 ||
		ev.HeadTitle != "feat: login con Google" || ev.Commits != 2 || ev.Actor != "dev-prueba" {
		t.Errorf("push: %+v", ev)
	}
	// Un tag no interesa.
	repoID, _, err = ParseWebhook("push", []byte(`{"ref":"refs/tags/v1","after":"`+sha1+`","repository":{"id":42}}`))
	if err != nil || repoID != 0 {
		t.Errorf("tag: %d %v", repoID, err)
	}
	// Borrado de rama.
	_, evs, _ = ParseWebhook("push", []byte(`{"ref":"refs/heads/feat/tk-9-login","after":"0000000000000000000000000000000000000000","deleted":true,"repository":{"id":42}}`))
	if len(evs) != 1 || !evs[0].Deleted || evs[0].HeadSHA != "" {
		t.Errorf("deleted: %+v", evs)
	}
	if _, _, err := ParseWebhook("push", []byte(`no json`)); !domainerrors.IsValidation(err) {
		t.Errorf("no json: %v", err)
	}
}

func TestParsePullRequest(t *testing.T) {
	body := `{"action":"opened","repository":{"id":42},"sender":{"login":"dev-prueba"},
	  "pull_request":{"number":7,"title":"Login (TK-9)","body":"cierra TK-12","state":"open","draft":false,
	    "merged":false,"updated_at":"2026-11-19T10:00:00Z","requested_reviewers":[{"login":"revisora"}],
	    "head":{"ref":"feat/tk-9-login","sha":"` + sha1 + `","repo":{"id":42}},"base":{"repo":{"id":42}}}}`
	repoID, evs, err := ParseWebhook("pull_request", []byte(body))
	if err != nil || repoID != 42 || len(evs) != 1 {
		t.Fatalf("got %d %v %v", repoID, evs, err)
	}
	pr := evs[0].PR
	if evs[0].Kind != model.RepoEventPR || evs[0].Branch != "feat/tk-9-login" || pr.Number != 7 ||
		pr.State != model.PRStateOpen || !pr.ReviewRequested || pr.HeadSHA != sha1 ||
		!pr.UpdatedAt.Equal(time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("pr: %+v %+v", evs[0], pr)
	}
	merged := strings.Replace(body, `"merged":false`, `"merged":true`, 1)
	merged = strings.Replace(merged, `"state":"open"`, `"state":"closed"`, 1)
	_, evs, _ = ParseWebhook("pull_request", []byte(merged))
	if evs[0].PR.State != model.PRStateMerged {
		t.Errorf("merged: %v", evs[0].PR.State)
	}
	// Un fork lleva el dueño delante de la rama.
	fork := strings.Replace(body, `"repo":{"id":42}},"base"`, `"repo":{"id":99,"owner":{"login":"otra"}}},"base"`, 1)
	_, evs, _ = ParseWebhook("pull_request", []byte(fork))
	if evs[0].Branch != "otra:feat/tk-9-login" {
		t.Errorf("fork: %q", evs[0].Branch)
	}
	// Un review solo cambia la revisión pedida.
	_, evs, _ = ParseWebhook("pull_request_review", []byte(body))
	if evs[0].Kind != model.RepoEventReview {
		t.Errorf("review: %v", evs[0].Kind)
	}
}

func TestParseCheckSuite(t *testing.T) {
	mk := func(status, conclusion string) string {
		return `{"action":"completed","repository":{"id":42},"sender":{"login":"github-actions"},
		  "check_suite":{"id":555,"head_branch":"feat/tk-9-login","head_sha":"` + sha2 + `",
		  "status":"` + status + `","conclusion":"` + conclusion + `"}}`
	}
	cases := map[string]model.CIStatus{
		mk("completed", "failure"):   model.CIStatusFailed,
		mk("completed", "success"):   model.CIStatusSuccess,
		mk("completed", "cancelled"): model.CIStatusCanceled,
		mk("in_progress", ""):        model.CIStatusPending,
	}
	for body, want := range cases {
		repoID, evs, err := ParseWebhook("check_suite", []byte(body))
		if err != nil || repoID != 42 || len(evs) != 1 {
			t.Fatalf("got %d %v %v", repoID, evs, err)
		}
		if evs[0].CIStatus != want || evs[0].SuiteID != "555" || evs[0].HeadSHA != sha2 {
			t.Errorf("%s: %+v", want, evs[0])
		}
	}
}

func TestParseOtherEvents(t *testing.T) {
	for _, ev := range []string{"ping", "installation", "issues", "star"} {
		repoID, evs, err := ParseWebhook(ev, []byte(`{"zen":"x","repository":{"id":42}}`))
		if err != nil || repoID != 0 || evs != nil {
			t.Errorf("%s: %d %v %v", ev, repoID, evs, err)
		}
	}
}

// testKey es una llave RSA generada en el test (nunca una real).
func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	return k, base64.StdEncoding.EncodeToString(p)
}

func TestAppAPI(t *testing.T) {
	key, b64 := testKey(t)
	var tokenCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/app/installations/77":
			checkJWT(t, key, auth)
			_, _ = w.Write([]byte(`{"id":77,"account":{"login":"acme"}}`))
		case r.URL.Path == "/app/installations/78":
			http.NotFound(w, r)
		case r.URL.Path == "/app/installations/77/access_tokens" && r.Method == http.MethodPost:
			checkJWT(t, key, auth)
			tokenCalls.Add(1)
			_, _ = w.Write([]byte(`{"token":"ghs_prueba","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
		case r.URL.Path == "/installation/repositories":
			if auth != "token ghs_prueba" {
				t.Errorf("auth repos: %q", auth)
			}
			_, _ = w.Write([]byte(`{"total_count":2,"repositories":[
			  {"id":1,"name":"api","default_branch":"main","topics":["tekitla"],"owner":{"login":"acme"}},
			  {"id":2,"name":"../malo","default_branch":"main","owner":{"login":"acme"}}]}`))
		case r.URL.Path == "/repos/acme/api/pulls":
			_, _ = w.Write([]byte(`[{"number":3,"title":"TK-9","state":"open","user":{"login":"dev-prueba"},
			  "head":{"ref":"feat/tk-9-login","sha":"` + sha1 + `","repo":{"id":1}},"base":{"repo":{"id":1}}}]`))
		case r.URL.Path == "/redirige":
			http.Redirect(w, r, "https://ejemplo.invalid/", http.StatusFound)
		default:
			http.Error(w, "x", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	pemBytes, err := DecodePrivateKey(b64)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(Config{AppID: 123, Slug: "tekitla-qa", PrivateKeyPEM: pemBytes,
		WebhookSecret: []byte("secreto-de-prueba-0123456789"), APIBase: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if owner, err := app.Installation(ctx, 77); err != nil || owner != "acme" {
		t.Errorf("Installation: %q %v", owner, err)
	}
	if _, err := app.Installation(ctx, 78); !domainerrors.IsNotFound(err) {
		t.Errorf("Installation 78: %v", err)
	}
	repos, err := app.ListRepos(ctx, 77)
	if err != nil || len(repos) != 1 || repos[0].Name != "api" || repos[0].Topics[0] != "tekitla" {
		t.Errorf("ListRepos: %+v %v", repos, err)
	}
	prs, err := app.ListOpenPRs(ctx, 77, "acme", "api")
	if err != nil || len(prs) != 1 || prs[0].PR.Number != 3 || prs[0].Actor != "dev-prueba" {
		t.Errorf("ListOpenPRs: %+v %v", prs, err)
	}
	if tokenCalls.Load() != 1 {
		t.Errorf("token de instalación pedido %d veces, want 1 (caché)", tokenCalls.Load())
	}
	// Las redirecciones no se siguen: es una falla, no otra URL.
	var out map[string]any
	if err := app.do(ctx, http.MethodGet, "/redirige", "token x", &out); err == nil {
		t.Error("redirect: want error")
	}
	if app.InstallURL() != "https://github.com/apps/tekitla-qa/installations/new" {
		t.Errorf("InstallURL: %s", app.InstallURL())
	}
}

func checkJWT(t *testing.T, key *rsa.PrivateKey, auth string) {
	t.Helper()
	jwt, ok := strings.CutPrefix(auth, "Bearer ")
	parts := strings.Split(jwt, ".")
	if !ok || len(parts) != 3 {
		t.Fatalf("JWT: %q", auth)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Errorf("JWT signature: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
		Iat int64  `json:"iat"`
	}
	_ = json.Unmarshal(raw, &claims)
	if claims.Iss != "123" || claims.Exp-claims.Iat > 10*60 {
		t.Errorf("claims: %+v", claims)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	_, b64 := testKey(t)
	pemBytes, _ := DecodePrivateKey(b64)
	good := Config{AppID: 1, Slug: "tekitla", PrivateKeyPEM: pemBytes, WebhookSecret: []byte("secreto-de-prueba-0123456789")}
	bad := map[string]func(c *Config){
		"sin id":        func(c *Config) { c.AppID = 0 },
		"slug raro":     func(c *Config) { c.Slug = "Tekitla App" },
		"secreto corto": func(c *Config) { c.WebhookSecret = []byte("corto") },
		"llave no pem":  func(c *Config) { c.PrivateKeyPEM = []byte("hola") },
	}
	for name, mut := range bad {
		c := good
		mut(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := New(good); err != nil {
		t.Errorf("good: %v", err)
	}
}

package model

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// Integración con GitHub y GitLab (issue 016): conexiones, repos de un
// proyecto y links de ramas y PRs a issues. El plan del issue (repo docs) es
// la especificación; el comentario de integrations.proto, el contrato.

// RepoProvider es el proveedor de git. En la base, el texto.
type RepoProvider string

const (
	RepoProviderUnspecified RepoProvider = ""
	RepoProviderGitHub      RepoProvider = "github"
	RepoProviderGitLab      RepoProvider = "gitlab"
)

// ConnectionScope es qué repos de la organización o grupo entran.
type ConnectionScope string

const (
	ConnectionScopeUnspecified ConnectionScope = ""
	ConnectionScopeAll         ConnectionScope = "all"
	ConnectionScopeTopic       ConnectionScope = "topic"
	ConnectionScopeSelected    ConnectionScope = "selected"
)

// IsValid informa si el alcance es uno de los definidos.
func (s ConnectionScope) IsValid() bool {
	return s == ConnectionScopeAll || s == ConnectionScopeTopic || s == ConnectionScopeSelected
}

// GitLabSyncMode es cómo llegan los cambios de un grupo de GitLab (plan §3.2).
type GitLabSyncMode string

const (
	GitLabSyncModeUnspecified GitLabSyncMode = ""
	GitLabSyncModeWebhook     GitLabSyncMode = "webhook"
	GitLabSyncModePoll        GitLabSyncMode = "poll"
)

// PRState es el estado del PR (o MR) de una rama. En la base, el texto.
type PRState string

const (
	PRStateNone   PRState = "none"
	PRStateDraft  PRState = "draft"
	PRStateOpen   PRState = "open"
	PRStateMerged PRState = "merged"
	PRStateClosed PRState = "closed"
)

// CIStatus es el estado del CI del último commit de una rama.
type CIStatus string

const (
	CIStatusNone     CIStatus = "none"
	CIStatusPending  CIStatus = "pending"
	CIStatusSuccess  CIStatus = "success"
	CIStatusFailed   CIStatus = "failed"
	CIStatusCanceled CIStatus = "canceled"
)

// LinkOrigin es por qué se ligó la rama al issue.
type LinkOrigin string

const (
	LinkOriginBranchName LinkOrigin = "branch_name"
	LinkOriginPRText     LinkOrigin = "pr_text"
	LinkOriginManual     LinkOrigin = "manual"
)

// Límites de la integración.
const (
	MaxConnectionsPerProject  = 10
	MaxRepositoriesPerProject = 300
	MaxLinksPerIssue          = 20
	// MaxProjectLinks es el tope de ListIssueLinks del proyecto (tablero).
	MaxProjectLinks = 2000
	// HeadTitleMaxLen es el largo de la primera línea del commit que se
	// guarda (decisión 7 de Victor).
	HeadTitleMaxLen = 120
	PRTitleMaxLen   = 200
	BranchMaxLen    = 255
	UpdatedByMaxLen = 100
	DeliveryIDMax   = 100
	TopicMaxLen     = 50
	// DeliveryRetention es cuánto se guardan los ids de entrega.
	DeliveryRetention = 30 * 24 * time.Hour
	// WebhookSecretPrefix va delante del secreto de los webhooks de GitLab.
	WebhookSecretPrefix = "tkw_"
)

// Connection es una conexión de organización (GitHub) o grupo (GitLab), sin
// secretos: el token de GitLab vive en una variable de entorno
// (CredentialRef) y del secreto del webhook solo se guarda el hash.
type Connection struct {
	ID              string
	ProjectID       string
	Provider        RepoProvider
	Host            string
	Owner           string
	InstallationID  int64
	CredentialRef   string
	SyncMode        GitLabSyncMode
	PollIntervalMin int
	Scope           ConnectionScope
	Topic           string
	Selected        []string
	CreatedByUID    string
	CreatedAt       time.Time
	LastDeliveryAt  *time.Time
	RepositoryCount int
}

// Repository es un repo conectado a un proyecto.
type Repository struct {
	ID            string
	ProjectID     string
	ConnectionID  string
	Provider      RepoProvider
	Host          string
	ExternalID    int64
	Owner         string
	Name          string
	DefaultBranch string
	Kind          string // "code" | "docs"
}

// Source es cómo llegó el repo: por organización (GitHub) o grupo (GitLab).
func (r *Repository) Source() string {
	if r.Provider == RepoProviderGitLab {
		return "group"
	}
	return "org"
}

// WebURL arma la URL del repo con el host de la conexión (nunca la de un
// payload).
func (r *Repository) WebURL() string {
	return "https://" + r.Host + "/" + r.Owner + "/" + r.Name
}

// BranchURL es la URL de una rama del repo.
func (r *Repository) BranchURL(branch string) string {
	if r.Provider == RepoProviderGitLab {
		return r.WebURL() + "/-/tree/" + branch
	}
	return r.WebURL() + "/tree/" + branch
}

// PRURL es la URL de un PR (o MR) del repo.
func (r *Repository) PRURL(number int) string {
	if r.Provider == RepoProviderGitLab {
		return r.WebURL() + "/-/merge_requests/" + strconv.Itoa(number)
	}
	return r.WebURL() + "/pull/" + strconv.Itoa(number)
}

// RemoteRepo es un repo tal como lo lista el proveedor.
type RemoteRepo struct {
	ExternalID    int64
	Owner         string
	Name          string
	DefaultBranch string
	Topics        []string
	Archived      bool
}

// SelectRepos aplica el alcance de la conexión a la lista del proveedor: sin
// archivados, por topic o por nombre, ordenados por nombre y con el tope por
// proyecto.
func SelectRepos(remote []RemoteRepo, scope ConnectionScope, topic string, selected []string) []RemoteRepo {
	want := map[string]bool{}
	for _, name := range selected {
		want[strings.ToLower(name)] = true
	}
	var out []RemoteRepo
	for _, r := range remote {
		if r.Archived {
			continue
		}
		switch scope {
		case ConnectionScopeTopic:
			has := false
			for _, t := range r.Topics {
				if strings.EqualFold(t, topic) {
					has = true
					break
				}
			}
			if !has {
				continue
			}
		case ConnectionScopeSelected:
			if !want[strings.ToLower(r.Name)] {
				continue
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	if len(out) > MaxRepositoriesPerProject {
		out = out[:MaxRepositoriesPerProject]
	}
	return out
}

var (
	topicRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,49}$`)
	repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// NormalizeScope valida el alcance de una conexión y devuelve el topic y la
// selección normalizados (vacíos si no aplican).
func NormalizeScope(scope ConnectionScope, topic string, selected []string) (string, []string, error) {
	switch scope {
	case ConnectionScopeAll:
		return "", nil, nil
	case ConnectionScopeTopic:
		topic = strings.ToLower(strings.TrimSpace(topic))
		if !topicRe.MatchString(topic) {
			return "", nil, domainerrors.ValidationError("topic must be 1-50 lowercase letters, digits or dashes",
				map[string]any{"field": "topic"})
		}
		return topic, nil, nil
	case ConnectionScopeSelected:
		if len(selected) == 0 || len(selected) > MaxRepositoriesPerProject {
			return "", nil, domainerrors.ValidationError("selected must have 1 to 300 repository names",
				map[string]any{"field": "selected"})
		}
		seen := map[string]bool{}
		out := make([]string, 0, len(selected))
		for _, name := range selected {
			name = strings.TrimSpace(name)
			if !repoNameRe.MatchString(name) {
				return "", nil, domainerrors.ValidationError("invalid repository name", map[string]any{"field": "selected"})
			}
			if key := strings.ToLower(name); !seen[key] {
				seen[key] = true
				out = append(out, name)
			}
		}
		return "", out, nil
	default:
		return "", nil, domainerrors.ValidationError("scope is required", map[string]any{"field": "scope"})
	}
}

// IssueLink es una rama (y su PR) ligada a un issue.
type IssueLink struct {
	ID              string
	IssueID         string
	IssueNumber     int32
	Repo            *Repository
	Branch          string
	HeadSHA         string
	HeadTitle       string
	Commits         int
	PRNumber        int
	PRTitle         string
	PRState         PRState
	PRUpdatedAt     *time.Time
	ReviewRequested bool
	CIStatus        CIStatus
	// CISuites es el estado de cada check suite (o pipeline) del head, por
	// id: ci_status es su agregado (AggregateCI).
	CISuites      map[string]CIStatus
	Origin        LinkOrigin
	Suppressed    bool
	BranchDeleted bool
	UpdatedBy     string
	UpdatedAt     time.Time
}

// WebURL es la URL del PR si lo tiene; si no, la de la rama.
func (l *IssueLink) WebURL() string {
	if l.Repo == nil {
		return ""
	}
	if l.PRNumber > 0 {
		return l.Repo.PRURL(l.PRNumber)
	}
	return l.Repo.BranchURL(l.Branch)
}

// AggregateCI resume los estados de las suites de un commit: uno fallido
// gana, después uno pendiente, después uno cancelado; todos bien, bien; sin
// suites, none.
func AggregateCI(suites map[string]CIStatus) CIStatus {
	if len(suites) == 0 {
		return CIStatusNone
	}
	has := map[CIStatus]bool{}
	for _, s := range suites {
		has[s] = true
	}
	switch {
	case has[CIStatusFailed]:
		return CIStatusFailed
	case has[CIStatusPending]:
		return CIStatusPending
	case has[CIStatusCanceled]:
		return CIStatusCanceled
	default:
		return CIStatusSuccess
	}
}

// issuePrefixRe es «issue-<n>» en una rama (feat/issue-231-login), la
// convención de Round y Tekitla (paso 5 del plan del 002).
var issuePrefixRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])issue-([1-9][0-9]{0,8})(?:$|[^0-9])`)

// keyRefRe arma la expresión de «<CLAVE>-<n>» sin distinguir mayúsculas, con
// límites: ni letra ni dígito antes, ni dígito después.
func keyRefRe(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[^a-z0-9])` + regexp.QuoteMeta(key) + `-([1-9][0-9]{0,8})(?:$|[^0-9])`)
}

// BranchIssueRefs devuelve los números de issue que nombra una rama de un
// repo del proyecto key: «<KEY>-<n>» o «issue-<n>». Sin repetidos, en orden.
func BranchIssueRefs(key, branch string) []int32 {
	return collectRefs(branch, keyRefRe(key), issuePrefixRe)
}

// TextIssueRefs devuelve los números que nombra el título o la descripción de
// un PR: solo «<KEY>-<n>» (un «issue-3» en un texto es demasiado ambiguo).
func TextIssueRefs(key, text string) []int32 {
	return collectRefs(text, keyRefRe(key))
}

func collectRefs(s string, res ...*regexp.Regexp) []int32 {
	seen := map[int32]bool{}
	var out []int32
	for _, re := range res {
		// Las coincidencias se solapan en el separador («tk-9-tk-10»): se
		// busca desde cada posición en lugar de FindAll.
		for start := 0; start < len(s); {
			loc := re.FindStringSubmatchIndex(s[start:])
			if loc == nil {
				break
			}
			n, err := strconv.ParseInt(s[start+loc[2]:start+loc[3]], 10, 32)
			if err == nil && !seen[int32(n)] {
				seen[int32(n)] = true
				out = append(out, int32(n))
			}
			start += loc[3]
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Truncate recorta s a max runas, sin cortar una runa.
func Truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

// FirstLine es la primera línea de un mensaje de commit, recortada.
func FirstLine(msg string, max int) string {
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = msg[:i]
	}
	return Truncate(strings.TrimSpace(msg), max)
}

var (
	shaRe        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	deliveryIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)
)

// IsSHA informa si s es un sha de commit (40 hex en minúsculas).
func IsSHA(s string) bool { return shaRe.MatchString(s) }

// IsDeliveryID informa si s tiene forma de id de entrega.
func IsDeliveryID(s string) bool { return deliveryIDRe.MatchString(s) }

// ValidBranch informa si una rama se puede guardar: 1 a 255 caracteres, sin
// controles.
func ValidBranch(b string) bool {
	if b == "" || len(b) > BranchMaxLen || !utf8.ValidString(b) {
		return false
	}
	for _, r := range b {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// RepoEvent es lo que un webhook (o una consulta) dice de un repo, ya
// traducido del proveedor. Un solo camino de escritura para GitHub y GitLab.
type RepoEvent struct {
	Kind RepoEventKind
	// Login del proveedor que causó el evento.
	Actor string
	// Rama (sin refs/heads/).
	Branch string

	// Push.
	Deleted   bool
	HeadSHA   string
	HeadTitle string
	Commits   int

	// PR.
	PR *PREvent

	// CI.
	SuiteID  string
	CIStatus CIStatus
}

// RepoEventKind es el tipo de RepoEvent.
type RepoEventKind int

const (
	RepoEventPush RepoEventKind = iota + 1
	RepoEventPR
	// RepoEventReview solo actualiza la revisión pedida.
	RepoEventReview
	RepoEventCI
)

// PREvent son los datos de un PR (o MR) en un evento.
type PREvent struct {
	Number          int
	Title           string
	Body            string
	State           PRState
	HeadSHA         string
	ReviewRequested bool
	UpdatedAt       time.Time
}

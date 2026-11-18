package model

import (
	"regexp"
	"strings"
	"time"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// Enlaces de solo lectura de un proyecto (issue 009).

// ShareScope es lo que ve quien abre un enlace. En la base, el texto.
type ShareScope string

const (
	ShareScopeUnspecified ShareScope = ""
	// ShareScopePlan: solo la vista Plan (issue 039).
	ShareScopePlan ShareScope = "plan"
	// ShareScopePanel: tablero, detalle de issues y Plan.
	ShareScopePanel ShareScope = "panel"
)

// IsValid informa si el alcance es uno de los definidos.
func (s ShareScope) IsValid() bool { return s == ShareScopePlan || s == ShareScopePanel }

// ShareDuration es el vencimiento que se pide al crear un enlace.
type ShareDuration int

const (
	ShareDurationUnspecified ShareDuration = iota
	ShareDurationSevenDays
	ShareDurationThirtyDays
	ShareDurationNinetyDays
	// ShareDurationNever: vale hasta que se revoca.
	ShareDurationNever
)

// Límites y formato de los enlaces.
const (
	// ShareTokenPrefix va delante de los 32 bytes aleatorios en base64url.
	// Distinto del de los tokens de API (tka_): un token de enlace nunca es
	// una credencial.
	ShareTokenPrefix = "tks_"
	// ShareTokenSecretBytes es la entropía del token (256 bits).
	ShareTokenSecretBytes = 32
	// ShareNoteMaxLen es el largo máximo de la nota.
	ShareNoteMaxLen = 80
	// ShareOwnerNameMaxLen es el largo máximo del nombre del dueño.
	ShareOwnerNameMaxLen = 40
	// MaxActiveShareLinksPerProject es el tope de enlaces activos (sin
	// revocar ni vencer) de un proyecto.
	MaxActiveShareLinksPerProject = 20
)

// ShareLink es un enlace de solo lectura. Nunca lleva el token: se guarda
// solo su hash, que tampoco sale del repo.
type ShareLink struct {
	ID               string
	ProjectID        string
	Scope            ShareScope
	Note             string
	OwnerDisplayName string
	// CreatedByUID es el uid de quien lo creó (auditoría). Nunca sale del
	// servicio.
	CreatedByUID string
	CreatedAt    time.Time
	// ExpiresAt nil: sin vencimiento.
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// IsActive informa si el enlace no está revocado ni vencido en now.
func (l *ShareLink) IsActive(now time.Time) bool {
	return l.RevokedAt == nil && (l.ExpiresAt == nil || now.Before(*l.ExpiresAt))
}

// NormalizeShareNote recorta espacios y valida la nota (obligatoria).
func NormalizeShareNote(raw string) (string, error) {
	return requiredText("note", raw, ShareNoteMaxLen)
}

// NormalizeShareOwnerName recorta espacios y valida el nombre del dueño
// (opcional).
func NormalizeShareOwnerName(raw string) (string, error) {
	return optionalText("owner_display_name", raw, ShareOwnerNameMaxLen)
}

// ShareLinkExpiry calcula el vencimiento de un enlace nuevo (nil = sin
// vencimiento).
func ShareLinkExpiry(now time.Time, d ShareDuration) (*time.Time, error) {
	var days int
	switch d {
	case ShareDurationSevenDays:
		days = 7
	case ShareDurationThirtyDays:
		days = 30
	case ShareDurationNinetyDays:
		days = 90
	case ShareDurationNever:
		return nil, nil
	default:
		return nil, domainerrors.ValidationError("duration is required", map[string]any{"field": "duration"})
	}
	t := now.AddDate(0, 0, days)
	return &t, nil
}

// LooksLikeShareToken informa si s tiene la forma de un token de enlace
// (prefijo y largo), sin tocar la base.
func LooksLikeShareToken(s string) bool {
	// base64url sin padding de 32 bytes = 43 caracteres.
	return strings.HasPrefix(s, ShareTokenPrefix) && len(s) == len(ShareTokenPrefix)+43
}

// SharedActor es un asignado visto por un enlace: sin id ni uid.
type SharedActor struct {
	Kind        ActorKind
	DisplayName string
	Tool        AgentTool
}

// SharedIssue es un issue visto por un enlace: solo lo que se puede mostrar.
type SharedIssue struct {
	Number int32
	Title  string
	// Description solo con alcance panel.
	Description string
	// WaitingNote solo con alcance plan (ver WaitingNote).
	WaitingNote string
	State       IssueState
	Priority    Priority
	StatusID    string
	LabelIDs    []string
	// Assignee nil: sin asignar.
	Assignee  *SharedActor
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SharedProjectView es lo que GetSharedProject devuelve.
type SharedProjectView struct {
	Project  *Project
	Scope    ShareScope
	Statuses []*Status
	Labels   []*Label
}

// waitingNoteLine es `**Qué espera:** …`, con o sin `>` (cabecera de los
// docs de Round). La misma regla que waitingNote del panel (plan.logic.ts).
var waitingNoteLine = regexp.MustCompile(`(?i)^\s*(?:>\s*)?\*\*\s*qu[eé] espera\s*:\s*\*\*\s*(.*?)\s*$`)

// waitingNoteMarks son las negritas y comillas invertidas que se quitan.
var waitingNoteMarks = strings.NewReplacer("**", "", "`", "")

// WaitingNote devuelve lo que espera un issue: la primera línea
// `**Qué espera:** …` de la descripción antes del primer encabezado `## `,
// sin el rótulo, negritas ni comillas invertidas. "" si no hay (issue 039,
// CU-039-10).
func WaitingNote(description string) string {
	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "## ") {
			break
		}
		if m := waitingNoteLine.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(waitingNoteMarks.Replace(m[1]))
		}
	}
	return ""
}

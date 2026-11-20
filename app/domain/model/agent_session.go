package model

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// Sesiones de agentes (issue 026). El comentario de agent_sessions.proto y de
// agent_session_service.proto es la especificación.

// SessionStatus es el estado de una sesión. El string es el que se guarda en
// issues.agent_sessions.status, salvo SessionStalled, que no se guarda.
type SessionStatus string

// Estados de una sesión.
const (
	SessionWorking   SessionStatus = "working"
	SessionWaitingOK SessionStatus = "waiting_ok"
	SessionIdle      SessionStatus = "idle"
	SessionEnded     SessionStatus = "ended"
	SessionUnknown   SessionStatus = "unknown"
	// SessionStalled (colgado): WORKING sin reporte durante más del umbral.
	// Solo sale al leer (EffectiveStatus).
	SessionStalled SessionStatus = "stalled"
)

// IsStored informa si el estado se puede guardar (y reportar).
func (s SessionStatus) IsStored() bool {
	switch s {
	case SessionWorking, SessionWaitingOK, SessionIdle, SessionEnded, SessionUnknown:
		return true
	}
	return false
}

// IsValid informa si el estado es uno de los definidos (con STALLED).
func (s SessionStatus) IsValid() bool { return s.IsStored() || s == SessionStalled }

// WaitingOn es qué espera una sesión.
type WaitingOn string

// Qué espera una sesión.
const (
	WaitingNone    WaitingOn = "none"
	WaitingPerson  WaitingOn = "person"
	WaitingSession WaitingOn = "session"
	WaitingOther   WaitingOn = "other"
)

// IsValid informa si es uno de los definidos.
func (w WaitingOn) IsValid() bool {
	switch w {
	case WaitingNone, WaitingPerson, WaitingSession, WaitingOther:
		return true
	}
	return false
}

// WaitingReason es para qué espera una sesión a su persona (issue 036,
// token espera_motivo). Vacío: sin motivo.
type WaitingReason string

// Motivos de una espera.
const (
	WaitingReasonNone     WaitingReason = ""
	WaitingReasonReview   WaitingReason = "review"
	WaitingReasonTesting  WaitingReason = "testing"
	WaitingReasonDecision WaitingReason = "decision"
	WaitingReasonDeploy   WaitingReason = "deploy"
)

// IsValid informa si es uno de los definidos (también sin motivo).
func (r WaitingReason) IsValid() bool {
	switch r {
	case WaitingReasonNone, WaitingReasonReview, WaitingReasonTesting, WaitingReasonDecision, WaitingReasonDeploy:
		return true
	}
	return false
}

// SessionSource es de dónde sale una sesión.
type SessionSource string

// Orígenes de una sesión.
const SessionSourceHerdr SessionSource = "herdr"

// IsValid informa si es uno de los definidos.
func (s SessionSource) IsValid() bool { return s == SessionSourceHerdr }

// SessionLink es la regla que ligó la sesión a su proyecto e issue.
type SessionLink string

// Reglas de ligado (issue 026, §2).
const (
	SessionLinkNone      SessionLink = "none"
	SessionLinkAgentName SessionLink = "agent_name"
	SessionLinkWorkspace SessionLink = "workspace"
	SessionLinkCwd       SessionLink = "cwd"
)

// IsValid informa si es una de las definidas.
func (l SessionLink) IsValid() bool {
	switch l {
	case SessionLinkNone, SessionLinkAgentName, SessionLinkWorkspace, SessionLinkCwd:
		return true
	}
	return false
}

// Límites del reporte (agent_session_service.proto).
const (
	ReportMaxSessions       = 200
	SessionExternalIDMaxLen = 200
	SessionCwdMaxLen        = 500
	SessionNameMaxLen       = 200
	SessionWaitTargetMaxLen = 80
	SessionWaitTextMaxLen   = 200
	// SessionActiveWindow: una sesión sin reporte durante más deja de ser
	// activa (decisión 1 de Victor, plan del 026).
	SessionActiveWindow = 24 * time.Hour
	// DefaultSessionStallAfter: umbral de «colgado» por defecto
	// (SESSION_STALL_AFTER).
	DefaultSessionStallAfter = 10 * time.Minute
	// IssueSessionsMaxItems: tope de IssueSessions.Items (decisión 2B).
	IssueSessionsMaxItems = 5
)

var machineRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

// SessionKey identifica las sesiones de una máquina de una persona: lo que
// reemplaza un reporte.
type SessionKey struct {
	OwnerActorID string
	Source       SessionSource
	Machine      string
}

// AgentSession es una sesión guardada.
type AgentSession struct {
	ID               string
	OwnerActorID     string
	AgentActorID     string
	AgentDisplayName string
	Source           SessionSource
	Machine          string
	ExternalID       string
	Tool             AgentTool
	CwdRel           string
	SessionName      string
	WorkspaceLabel   string
	// ProjectID vacío e IssueNumber 0: sin issue.
	ProjectID       string
	IssueNumber     int32
	Link            SessionLink
	Status          SessionStatus
	WaitingOn       WaitingOn
	WaitingTarget   string
	WaitingText     string
	WaitingReason   WaitingReason
	StatusChangedAt time.Time
	LastSeenAt      time.Time
	StartedAt       time.Time
	// EndedAt nil = no terminó.
	EndedAt *time.Time
}

// IsActive informa si la sesión es activa en now: no terminó y tuvo un
// reporte dentro de SessionActiveWindow.
func (s *AgentSession) IsActive(now time.Time) bool {
	return s.Status != SessionEnded && now.Sub(s.LastSeenAt) <= SessionActiveWindow
}

// EffectiveStatus es el estado que se devuelve: STALLED si trabaja y no
// reporta hace más de stallAfter.
func (s *AgentSession) EffectiveStatus(now time.Time, stallAfter time.Duration) SessionStatus {
	if s.Status == SessionWorking && now.Sub(s.LastSeenAt) > stallAfter {
		return SessionStalled
	}
	return s.Status
}

// ReportedSession es lo que manda el recolector de una sesión, ya traducido.
// ProjectKey es la clave sin resolver; ProjectID lo llena el servicio.
type ReportedSession struct {
	ExternalID     string
	Tool           AgentTool
	CwdRel         string
	SessionName    string
	WorkspaceLabel string
	ProjectKey     string
	ProjectID      string
	IssueNumber    int32
	Link           SessionLink
	Status         SessionStatus
	WaitingOn      WaitingOn
	WaitingTarget  string
	WaitingText    string
	WaitingReason  WaitingReason
}

// NormalizeMachine valida el nombre de la máquina.
func NormalizeMachine(raw string) (string, error) {
	if !machineRe.MatchString(raw) {
		return "", domainerrors.ValidationError("machine must be 1 to 40 characters of [A-Za-z0-9._-]", map[string]any{"field": "machine"})
	}
	return raw, nil
}

// NormalizeReport valida un reporte completo y devuelve las sesiones
// normalizadas. Un error en cualquiera es ValidationError con su índice: no
// se guarda nada.
func NormalizeReport(source SessionSource, sessions []ReportedSession) ([]ReportedSession, error) {
	if !source.IsValid() {
		return nil, domainerrors.ValidationError("invalid source", map[string]any{"field": "source", "value": string(source)})
	}
	if len(sessions) > ReportMaxSessions {
		return nil, domainerrors.ValidationError("too many sessions", map[string]any{"field": "sessions", "max": ReportMaxSessions})
	}
	seen := make(map[string]bool, len(sessions))
	out := make([]ReportedSession, len(sessions))
	for i, s := range sessions {
		n, err := normalizeReported(s)
		if err != nil {
			return nil, withIndex(err, i)
		}
		if seen[n.ExternalID] {
			return nil, domainerrors.ValidationError("duplicate external_id", map[string]any{"field": "sessions.external_id", "index": i})
		}
		seen[n.ExternalID] = true
		out[i] = n
	}
	return out, nil
}

func withIndex(err error, i int) error {
	if de, ok := err.(*domainerrors.DomainError); ok {
		details := map[string]any{"index": i}
		for k, v := range de.Details {
			details[k] = v
		}
		return domainerrors.ValidationError(de.Message, details)
	}
	return err
}

func normalizeReported(s ReportedSession) (ReportedSession, error) {
	invalid := func(field, msg string) error {
		return domainerrors.ValidationError(msg, map[string]any{"field": "sessions." + field})
	}
	if s.ExternalID == "" || utf8.RuneCountInString(s.ExternalID) > SessionExternalIDMaxLen {
		return s, invalid("external_id", "external_id must be 1 to 200 characters")
	}
	if !s.Tool.IsValid() {
		return s, invalid("tool", "invalid tool")
	}
	cwd, err := NormalizeCwdRel(s.CwdRel)
	if err != nil {
		return s, err
	}
	s.CwdRel = cwd
	if s.SessionName, err = optionalText("sessions.session_name", s.SessionName, SessionNameMaxLen); err != nil {
		return s, err
	}
	if s.WorkspaceLabel, err = optionalText("sessions.workspace_label", s.WorkspaceLabel, SessionNameMaxLen); err != nil {
		return s, err
	}
	s.ProjectKey = strings.ToUpper(strings.TrimSpace(s.ProjectKey))
	switch {
	case s.IssueNumber < 0:
		return s, invalid("issue_number", "issue_number must not be negative")
	case s.ProjectKey == "" && s.IssueNumber != 0:
		return s, invalid("issue_number", "issue_number needs project_key")
	case s.ProjectKey != "" && s.IssueNumber == 0:
		return s, invalid("issue_number", "project_key needs issue_number")
	}
	if !s.Link.IsValid() {
		return s, invalid("link", "invalid link")
	}
	if (s.ProjectKey == "") != (s.Link == SessionLinkNone) {
		return s, invalid("link", "link must be NONE exactly when there is no project_key")
	}
	if !s.Status.IsStored() {
		return s, invalid("status", "invalid status")
	}
	if s.WaitingOn == "" {
		s.WaitingOn = WaitingNone
	}
	if !s.WaitingOn.IsValid() {
		return s, invalid("waiting_on", "invalid waiting_on")
	}
	if s.WaitingTarget, err = optionalText("sessions.waiting_target", s.WaitingTarget, SessionWaitTargetMaxLen); err != nil {
		return s, err
	}
	if s.WaitingText, err = optionalText("sessions.waiting_text", s.WaitingText, SessionWaitTextMaxLen); err != nil {
		return s, err
	}
	// Una sesión que trabaja (o terminó) no espera nada: un token de espera
	// de un turno anterior no cuenta (CU-026-12).
	if s.Status == SessionWorking || s.Status == SessionEnded {
		s.WaitingOn = WaitingNone
	}
	if s.WaitingOn != WaitingSession && s.WaitingOn != WaitingOther {
		s.WaitingTarget = ""
	}
	if !s.WaitingReason.IsValid() {
		return s, invalid("waiting_reason", "invalid waiting_reason")
	}
	if s.WaitingOn == WaitingNone {
		s.WaitingText = ""
		// esperando-ok sin token también espera a su persona (issue 036): su
		// motivo se conserva.
		if s.Status != SessionWaitingOK {
			s.WaitingReason = WaitingReasonNone
		}
	}
	return s, nil
}

// NormalizeCwdRel valida un directorio relativo: con "/", sin ".." ni ruta
// absoluta (ni letra de unidad), hasta SessionCwdMaxLen runas. Los "\" se
// pasan a "/".
func NormalizeCwdRel(raw string) (string, error) {
	invalid := domainerrors.ValidationError("cwd_rel must be a relative path without ..", map[string]any{"field": "sessions.cwd_rel"})
	if utf8.RuneCountInString(raw) > SessionCwdMaxLen {
		return "", invalid
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) {
		return "", invalid
	}
	if len(raw) >= 2 && raw[1] == ':' {
		return "", invalid
	}
	p := strings.ReplaceAll(raw, `\`, "/")
	if slices.Contains(strings.Split(p, "/"), "..") {
		return "", invalid
	}
	return p, nil
}

// ReportOutcome es el resultado de aplicar un reporte.
type ReportOutcome struct {
	Created, Updated, Unchanged, Ended int
	// Upserts son las sesiones que hay que escribir: nuevas, cambiadas y
	// también las sin cambios (por last_seen_at).
	Upserts []*AgentSession
	// EndedIDs son las que pasan a ENDED por no venir en el reporte.
	EndedIDs []string
	// Transitions son las filas de historia que escribe el reporte (issue
	// 036, PlanTransitions).
	Transitions []SessionTransition
}

// SessionAgent es el agente de una sesión nueva o reabierta: lo resuelve el
// servicio por herramienta.
type SessionAgent struct {
	ID          string
	DisplayName string
}

// PlanReport compara las sesiones guardadas de una máquina (existing, todas,
// también las terminadas) con el reporte (ya normalizado, con ProjectID
// resuelto) y decide qué escribir. newID da el id de una sesión nueva y
// agents el agente de cada herramienta. Las sesiones cuya herramienta no
// está en agents (el servicio ya las anotó como rechazadas) no se escriben
// ni se terminan: la guardada queda como estaba.
func PlanReport(key SessionKey, existing []*AgentSession, reported []ReportedSession, agents map[AgentTool]SessionAgent, now time.Time, newID func() string) ReportOutcome {
	byExt := make(map[string]*AgentSession, len(existing))
	for _, e := range existing {
		byExt[e.ExternalID] = e
	}
	var out ReportOutcome
	inReport := make(map[string]bool, len(reported))
	for _, r := range reported {
		// Vino en el reporte aunque no se pueda guardar (agente revocado):
		// no se termina, solo no se actualiza.
		inReport[r.ExternalID] = true
		agent, ok := agents[r.Tool]
		if !ok {
			continue
		}
		prev := byExt[r.ExternalID]
		next := &AgentSession{
			OwnerActorID:     key.OwnerActorID,
			AgentActorID:     agent.ID,
			AgentDisplayName: agent.DisplayName,
			Source:           key.Source,
			Machine:          key.Machine,
			ExternalID:       r.ExternalID,
			Tool:             r.Tool,
			CwdRel:           r.CwdRel,
			SessionName:      r.SessionName,
			WorkspaceLabel:   r.WorkspaceLabel,
			ProjectID:        r.ProjectID,
			IssueNumber:      r.IssueNumber,
			Link:             r.Link,
			Status:           r.Status,
			WaitingOn:        r.WaitingOn,
			WaitingTarget:    r.WaitingTarget,
			WaitingText:      r.WaitingText,
			WaitingReason:    r.WaitingReason,
			LastSeenAt:       now,
		}
		if r.ProjectID == "" {
			// Clave desconocida: sin issue (el servicio lo anota).
			next.IssueNumber = 0
			next.Link = SessionLinkNone
		}
		if r.Status == SessionEnded {
			next.EndedAt = &now
		}
		switch {
		case prev == nil:
			next.ID = newID()
			next.StartedAt = now
			next.StatusChangedAt = now
			out.Created++
		case prev.Status == SessionEnded && r.Status != SessionEnded:
			// Una terminada que vuelve a aparecer se reabre.
			next.ID = prev.ID
			next.StartedAt = now
			next.StatusChangedAt = now
			out.Updated++
		default:
			next.ID = prev.ID
			next.StartedAt = prev.StartedAt
			next.StatusChangedAt = prev.StatusChangedAt
			if prev.Status != next.Status {
				next.StatusChangedAt = now
			}
			if prev.EndedAt != nil && next.Status == SessionEnded {
				next.EndedAt = prev.EndedAt
			}
			if sameReported(prev, next) {
				out.Unchanged++
			} else {
				out.Updated++
			}
		}
		out.Upserts = append(out.Upserts, next)
	}
	for _, e := range existing {
		if e.Status != SessionEnded && !inReport[e.ExternalID] {
			out.EndedIDs = append(out.EndedIDs, e.ID)
			out.Ended++
		}
	}
	return out
}

// sameReported compara lo que puede cambiar con un reporte, sin
// last_seen_at.
func sameReported(a, b *AgentSession) bool {
	return a.AgentActorID == b.AgentActorID && a.Tool == b.Tool && a.CwdRel == b.CwdRel &&
		a.SessionName == b.SessionName && a.WorkspaceLabel == b.WorkspaceLabel &&
		a.ProjectID == b.ProjectID && a.IssueNumber == b.IssueNumber && a.Link == b.Link &&
		a.Status == b.Status && a.WaitingOn == b.WaitingOn && a.WaitingTarget == b.WaitingTarget &&
		a.WaitingText == b.WaitingText && a.WaitingReason == b.WaitingReason
}

// IssueSessions es el resumen de las sesiones activas de un issue.
type IssueSessions struct {
	Active, Working, Waiting, Stalled, Idle int
	NeedsPerson                             bool
	// Items: solo si se pidieron (GetIssue). Status es el efectivo.
	Items []*AgentSession
}

// SummarizeSessions arma el resumen de un issue con sus sesiones (de
// cualquier estado: filtra las activas). Devuelve nil si no hay activas.
// Con withItems, Items trae hasta IssueSessionsMaxItems con Status
// efectivo: primero las que esperan a una persona, después las colgadas,
// después por LastSeenAt descendente.
func SummarizeSessions(sessions []*AgentSession, now time.Time, stallAfter time.Duration, withItems bool) *IssueSessions {
	var sum IssueSessions
	var active []*AgentSession
	for _, s := range sessions {
		if !s.IsActive(now) {
			continue
		}
		c := *s
		c.Status = s.EffectiveStatus(now, stallAfter)
		active = append(active, &c)
		sum.Active++
		switch c.Status {
		case SessionWorking:
			sum.Working++
		case SessionWaitingOK:
			sum.Waiting++
		case SessionStalled:
			sum.Stalled++
		case SessionIdle:
			sum.Idle++
		}
		if c.WaitingOn == WaitingPerson {
			sum.NeedsPerson = true
		}
	}
	if sum.Active == 0 {
		return nil
	}
	if withItems {
		rank := func(s *AgentSession) int {
			switch {
			case s.WaitingOn == WaitingPerson:
				return 0
			case s.Status == SessionStalled:
				return 1
			}
			return 2
		}
		slices.SortStableFunc(active, func(a, b *AgentSession) int {
			if ra, rb := rank(a), rank(b); ra != rb {
				return ra - rb
			}
			if c := b.LastSeenAt.Compare(a.LastSeenAt); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
		if len(active) > IssueSessionsMaxItems {
			active = active[:IssueSessionsMaxItems]
		}
		sum.Items = active
	}
	return &sum
}

// AgentNameForSession es el nombre del agente de una herramienta en una
// máquina (decisión 4): «Claude Code PC-01».
func AgentNameForSession(tool AgentTool, machine string) string {
	label := map[AgentTool]string{
		AgentToolClaudeCode: "Claude Code",
		AgentToolCodex:      "Codex",
		AgentToolCloud:      "Cloud",
		AgentToolOther:      "Agente",
	}[tool]
	return label + " " + machine
}

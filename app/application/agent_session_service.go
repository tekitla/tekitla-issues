package application

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// AgentSessionService implementa las sesiones de agentes (issue 026): el
// reporte del recolector local, la lista y el resumen de cada issue. Los
// comentarios de agent_session_service.proto son la especificación.
type AgentSessionService struct {
	ws         workspaces
	actors     actorAccess
	sessions   port.AgentSessionRepository
	stallAfter time.Duration
	now        func() time.Time
}

// NewAgentSessionService construye el servicio. stallAfter <= 0 usa
// model.DefaultSessionStallAfter.
func NewAgentSessionService(workspaceRepo port.WorkspaceRepository, actors port.ActorRepository, sessions port.AgentSessionRepository, stallAfter time.Duration) *AgentSessionService {
	if stallAfter <= 0 {
		stallAfter = model.DefaultSessionStallAfter
	}
	return &AgentSessionService{
		ws:         workspaces{repo: workspaceRepo},
		actors:     actorAccess{repo: actors},
		sessions:   sessions,
		stallAfter: stallAfter,
		now:        utcNow,
	}
}

// WithClock reemplaza el reloj (tests).
func (s *AgentSessionService) WithClock(now func() time.Time) *AgentSessionService {
	s.now = now
	return s
}

// ReportInput es un reporte completo de una máquina.
type ReportInput struct {
	Source   model.SessionSource
	Machine  string
	Sessions []model.ReportedSession
}

// SessionRejection es una sesión del reporte con un problema.
type SessionRejection struct {
	ExternalID string
	Saved      bool
	Reason     string
}

// ReportResult es lo que responde ReportSessions.
type ReportResult struct {
	Created, Updated, Unchanged, Ended int
	Rejected                           []SessionRejection
}

// Report reemplaza las sesiones de una máquina de la persona uid. El handler
// ya rechazó a un agente (x-agent-id).
func (s *AgentSessionService) Report(ctx context.Context, uid string, in ReportInput) (*ReportResult, error) {
	now := s.now()
	machine, err := model.NormalizeMachine(in.Machine)
	if err != nil {
		return nil, err
	}
	reported, err := model.NormalizeReport(in.Source, in.Sessions)
	if err != nil {
		return nil, err
	}
	person, err := s.actors.person(ctx, uid, now)
	if err != nil {
		return nil, err
	}
	if person.IsRevoked() {
		return nil, actorRevoked(person.ID)
	}
	res := &ReportResult{}

	// Proyectos: una clave desconocida deja la sesión sin issue.
	if err := s.resolveProjects(ctx, uid, reported, res); err != nil {
		return nil, err
	}

	// Agentes: uno por herramienta y máquina (decisión 4).
	agents := map[model.AgentTool]model.SessionAgent{}
	for _, r := range reported {
		if _, done := agents[r.Tool]; done {
			continue
		}
		agent, reason, err := s.agentFor(ctx, person, r.Tool, machine, now)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			for _, rr := range reported {
				if rr.Tool == r.Tool {
					res.Rejected = append(res.Rejected, SessionRejection{ExternalID: rr.ExternalID, Reason: reason})
				}
			}
			agents[r.Tool] = model.SessionAgent{} // marca: ya resuelta
			continue
		}
		agents[r.Tool] = model.SessionAgent{ID: agent.ID, DisplayName: agent.DisplayName}
	}
	for tool, a := range agents {
		if a.ID == "" {
			delete(agents, tool)
		}
	}

	key := model.SessionKey{OwnerActorID: person.ID, Source: in.Source, Machine: machine}
	out, err := s.sessions.Report(ctx, key, now, func(existing []*model.AgentSession) model.ReportOutcome {
		return model.PlanReport(key, existing, reported, agents, now, uuid.NewString)
	})
	if err != nil {
		return nil, err
	}
	res.Created, res.Updated, res.Unchanged, res.Ended = out.Created, out.Updated, out.Unchanged, out.Ended
	return res, nil
}

// resolveProjects llena ProjectID de cada sesión con clave y anota las
// claves desconocidas.
func (s *AgentSessionService) resolveProjects(ctx context.Context, uid string, reported []model.ReportedSession, res *ReportResult) error {
	var keys []string
	for _, r := range reported {
		if r.ProjectKey != "" && !slices.Contains(keys, r.ProjectKey) {
			keys = append(keys, r.ProjectKey)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	wsIDs, err := s.ws.ids(ctx, uid)
	if err != nil {
		return err
	}
	ids, err := s.sessions.ProjectIDsByKey(ctx, wsIDs, keys)
	if err != nil {
		return err
	}
	for i := range reported {
		r := &reported[i]
		if r.ProjectKey == "" {
			continue
		}
		if id, ok := ids[r.ProjectKey]; ok {
			r.ProjectID = id
			continue
		}
		res.Rejected = append(res.Rejected, SessionRejection{
			ExternalID: r.ExternalID,
			Saved:      true,
			Reason:     "proyecto " + r.ProjectKey + " desconocido: se guardó sin issue",
		})
	}
	return nil
}

// agentFor busca o crea el agente de la persona para tool en machine.
// Devuelve un motivo (y ningún agente) si sus sesiones no se pueden guardar.
func (s *AgentSessionService) agentFor(ctx context.Context, person *model.Actor, tool model.AgentTool, machine string, now time.Time) (*model.Actor, string, error) {
	name := model.AgentNameForSession(tool, machine)
	for attempt := 0; attempt < 2; attempt++ {
		agent, err := s.sessions.FindAgent(ctx, person.ID, tool, name)
		switch {
		case err == nil && agent.IsRevoked():
			return nil, "agente «" + name + "» revocado: crea otro con ese nombre desde el panel", nil
		case err == nil:
			return agent, "", nil
		case !domainerrors.IsNotFound(err):
			return nil, "", err
		}
		agent, err = s.actors.repo.CreateAgent(ctx, &model.Actor{
			ID:           uuid.NewString(),
			Kind:         model.ActorKindAgent,
			OwnerActorID: person.ID,
			Tool:         tool,
			DisplayName:  name,
			CreatedAt:    now,
		})
		if err == nil {
			return agent, "", nil
		}
		if !domainerrors.IsConflict(err) {
			return nil, "", err
		}
		// Conflicto de nombre: otro reporte lo creó recién (se vuelve a
		// buscar) o ya hay un agente con ese nombre y otra herramienta.
	}
	return nil, "ya hay un agente «" + name + "» con otra herramienta", nil
}

// ListSessionsInput son los filtros de ListAgentSessions.
type ListSessionsInput struct {
	ProjectID       string
	IssueNumber     int32
	Statuses        []model.SessionStatus
	AgentActorID    string
	IncludeInactive bool
	Unlinked        bool
	Page            port.PageFilter
}

// List devuelve las sesiones de la persona uid con el estado efectivo.
func (s *AgentSessionService) List(ctx context.Context, uid string, in ListSessionsInput) (*port.AgentSessionPage, error) {
	now := s.now()
	f := port.AgentSessionFilter{IssueNumber: in.IssueNumber, Unlinked: in.Unlinked, Page: port.PageFilter{Cursor: in.Page.Cursor, Limit: normalizeLimit(in.Page.Limit)}}
	var err error
	if in.ProjectID != "" {
		if f.ProjectID, err = canonicalUUID("project_id", in.ProjectID); err != nil {
			return nil, err
		}
	}
	if in.IssueNumber < 0 || (in.IssueNumber > 0 && f.ProjectID == "") {
		return nil, domainerrors.ValidationError("issue_number needs project_id", map[string]any{"field": "issue_number"})
	}
	if in.Unlinked && f.ProjectID != "" {
		return nil, domainerrors.ValidationError("unlinked cannot be combined with project_id", map[string]any{"field": "unlinked"})
	}
	if in.AgentActorID != "" {
		if f.AgentActorID, err = canonicalUUID("agent_actor_id", in.AgentActorID); err != nil {
			return nil, err
		}
	}
	// STALLED es WORKING con last_seen_at viejo: se piden los WORKING y se
	// filtra después. Con WORKING sin STALLED, igual (se quitan las colgadas).
	wantStalled := slices.Contains(in.Statuses, model.SessionStalled)
	wantWorking := slices.Contains(in.Statuses, model.SessionWorking)
	for _, st := range in.Statuses {
		if !st.IsValid() {
			return nil, domainerrors.ValidationError("invalid status", map[string]any{"field": "statuses", "value": string(st)})
		}
		if st == model.SessionEnded && !in.IncludeInactive {
			return nil, domainerrors.ValidationError("ENDED needs include_inactive", map[string]any{"field": "statuses"})
		}
		stored := st
		if st == model.SessionStalled {
			stored = model.SessionWorking
		}
		if !slices.Contains(f.StoredStatuses, stored) {
			f.StoredStatuses = append(f.StoredStatuses, stored)
		}
	}
	if !in.IncludeInactive {
		f.ActiveSince = now.Add(-model.SessionActiveWindow)
	}
	person, err := s.actors.person(ctx, uid, now)
	if err != nil {
		return nil, err
	}
	f.OwnerActorID = person.ID
	page, err := s.sessions.List(ctx, f)
	if err != nil {
		return nil, err
	}
	filterStalled := len(in.Statuses) > 0 && wantStalled != wantWorking
	kept := page.Sessions[:0]
	for _, ss := range page.Sessions {
		ss.Status = ss.EffectiveStatus(now, s.stallAfter)
		if filterStalled && ss.Status == model.SessionStalled && !wantStalled {
			continue
		}
		if filterStalled && ss.Status == model.SessionWorking && !wantWorking {
			continue
		}
		kept = append(kept, ss)
	}
	page.Sessions = kept
	return page, nil
}

// Summaries devuelve el resumen de sesiones de cada issue de numbers en
// projectID (los que no tienen sesiones activas no vienen). El llamador ya
// verificó que el proyecto es visible para el usuario.
func (s *AgentSessionService) Summaries(ctx context.Context, projectID string, numbers []int32, withItems bool) (map[int32]*model.IssueSessions, error) {
	now := s.now()
	sessions, err := s.sessions.ListForIssues(ctx, projectID, numbers, now.Add(-model.SessionActiveWindow))
	if err != nil {
		return nil, err
	}
	byIssue := map[int32][]*model.AgentSession{}
	for _, ss := range sessions {
		byIssue[ss.IssueNumber] = append(byIssue[ss.IssueNumber], ss)
	}
	out := make(map[int32]*model.IssueSessions, len(byIssue))
	for n, list := range byIssue {
		if sum := model.SummarizeSessions(list, now, s.stallAfter, withItems); sum != nil {
			out[n] = sum
		}
	}
	return out, nil
}

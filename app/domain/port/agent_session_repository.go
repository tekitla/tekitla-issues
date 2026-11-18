package port

import (
	"context"
	"time"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// AgentSessionRepository guarda las sesiones de agentes (issue 026).
type AgentSessionRepository interface {
	// ProjectIDsByKey resuelve claves de proyecto (en mayúsculas) entre los
	// proyectos de workspaceIDs. Las que no existen no vienen en el mapa.
	ProjectIDsByKey(ctx context.Context, workspaceIDs []string, keys []string) (map[string]string, error)
	// FindAgent devuelve el agente de ownerID con ese nombre (sin
	// mayúsculas) y herramienta, prefiriendo uno sin revocar. NotFound si no
	// hay ninguno.
	FindAgent(ctx context.Context, ownerID string, tool model.AgentTool, name string) (*model.Actor, error)
	// Report aplica un reporte a las sesiones de key en una transacción,
	// serializada por key: lee todas las sesiones de la máquina (también
	// las terminadas), llama a plan y escribe lo que devuelve. now es el
	// ended_at de las que no vinieron.
	Report(ctx context.Context, key model.SessionKey, now time.Time, plan func(existing []*model.AgentSession) model.ReportOutcome) (model.ReportOutcome, error)
	// List devuelve las sesiones de ownerID con filtro, por last_seen_at
	// descendente (id de desempate).
	List(ctx context.Context, filter AgentSessionFilter) (*AgentSessionPage, error)
	// ListForIssues devuelve las sesiones no terminadas de esos issues de un
	// proyecto con reporte desde activeSince, de cualquier persona.
	ListForIssues(ctx context.Context, projectID string, numbers []int32, activeSince time.Time) ([]*model.AgentSession, error)
}

// AgentSessionFilter son los filtros de ListAgentSessions ya validados.
type AgentSessionFilter struct {
	OwnerActorID string
	ProjectID    string
	IssueNumber  int32
	AgentActorID string
	Unlinked     bool
	// StoredStatuses: estados guardados a traer (vacío = todos). STALLED se
	// resuelve en el servicio: pide WORKING y filtra después.
	StoredStatuses []model.SessionStatus
	// ActiveSince: sin terminadas y con last_seen_at desde acá. Cero = todas.
	ActiveSince time.Time
	Page        PageFilter
}

// AgentSessionPage es una página de sesiones.
type AgentSessionPage struct {
	Sessions   []*model.AgentSession
	NextCursor string
	HasNext    bool
}

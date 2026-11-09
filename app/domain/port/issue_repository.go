package port

import (
	"context"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// IssueFilter parametriza ListIssues: los filtros se combinan con Y; dentro
// de una lista, con O. Una lista vacía no filtra. Todo viene validado (los
// estados y etiquetas son del proyecto, sin repetidos).
type IssueFilter struct {
	ProjectID  string
	States     []model.IssueState
	StatusIDs  []string
	Priorities []model.Priority
	// LabelIDs: pasa un issue con cualquiera de ellas.
	LabelIDs []string
	Search   model.IssueSearch
	// AssigneeActorIDs y Unassigned filtran por asignado, con O entre sí:
	// pasa un issue asignado a cualquiera de los actores (UUIDs válidos) o,
	// con Unassigned, uno sin asignar. Los dos vacíos no filtran.
	AssigneeActorIDs []string
	Unassigned       bool
	// Order nunca vacío (el service pone IssueOrderRank).
	Order  model.IssueOrder
	Cursor string
	Limit  int32
}

// IssuePage es una página de issues con cursor keyset.
type IssuePage struct {
	Issues     []*model.Issue
	NextCursor string
	HasNext    bool
}

// Placement ubica un issue dentro de su columna: debajo de AfterNumber y
// arriba de BeforeNumber, tal como el cliente ve la columna (0 = sin vecino
// de ese lado). Los dos en 0 = conserva su rank.
type Placement struct {
	AfterNumber  int32
	BeforeNumber int32
}

// IsZero informa si no pide reubicar.
func (p Placement) IsZero() bool { return p.AfterNumber == 0 && p.BeforeNumber == 0 }

// EventPage es una página del historial de un issue.
type EventPage struct {
	Events     []*model.IssueEvent
	NextCursor string
	HasNext    bool
}

// IssueRepository define la persistencia de Issue, sus comentarios y su
// historial. Toda escritura registra su evento en la misma transacción.
//
// La pertenencia del proyecto a los espacios del usuario la verifica el
// service (con ProjectRepository.GetByID) antes de llamar a estos métodos;
// Create la vuelve a exigir porque reserva el número sobre la fila del proyecto.
//
// Contrato de errores (todas las implementaciones):
//   - no existe              -> domainerrors.NotFoundError
//   - unique violation       -> domainerrors.ConflictError
//   - cursor inválido        -> domainerrors.ValidationError
//   - vecinos de Move viejos -> domainerrors.FailedPreconditionError (reason STALE_ORDER)
//   - cursor de otros filtros u otro orden -> domainerrors.ValidationError
//   - descartar uno descartado o restaurar uno que no lo está -> FailedPreconditionError
//   - cualquier otra cosa    -> domainerrors.InternalError
type IssueRepository interface {
	// Create asigna el siguiente número del proyecto y un rank al final del
	// orden (seguro con concurrencia), inserta el issue (StatusID y State ya
	// seteados) y su evento «creado» en la misma transacción. Si el proyecto
	// no existe o no es de uno de workspaceIDs devuelve NotFound. Devuelve el
	// issue con Number, Rank y timestamps seteados.
	Create(ctx context.Context, workspaceIDs []string, issue *model.Issue, author model.Author) (*model.Issue, error)
	// GetByNumber busca un issue por su número dentro del proyecto.
	GetByNumber(ctx context.Context, projectID string, number int32) (*model.Issue, error)
	// List pagina los issues del proyecto con los filtros y el orden de filter
	// (empates por rank ASC).
	List(ctx context.Context, filter IssueFilter) (*IssuePage, error)
	// Move pasa el issue al estado target (del mismo proyecto) y, si place no
	// es cero, le calcula un rank entre sus vecinos. Registra el cambio de
	// estado (reordenar no genera evento). Sin cambios, devuelve el issue tal
	// cual. Si el issue sale de CANCELED, borra su Discard.
	Move(ctx context.Context, projectID string, number int32, target *model.Status, place Placement, author model.Author) (*model.Issue, error)
	// Update aplica el patch (ya validado; las etiquetas son del proyecto) y
	// registra un evento por cada campo que de verdad cambia.
	Update(ctx context.Context, projectID string, number int32, patch model.IssuePatch, author model.Author) (*model.Issue, error)
	// Discard pasa el issue al estado CANCELED statusID (ya validado; "" = el
	// primero de esa categoría, elegido con el lock del proyecto tomado) sin
	// cambiar su rank, guarda el motivo y registra el evento «descartado».
	// FailedPrecondition (reason ISSUE_ALREADY_DISCARDED) si ya está en un
	// estado CANCELED.
	Discard(ctx context.Context, projectID string, number int32, statusID, reason string, author model.Author) (*model.Issue, error)
	// Restore pasa el issue al primer estado TODO del proyecto (elegido con
	// el lock tomado), al final de esa columna, borra su Discard y registra el
	// evento «restaurado». FailedPrecondition (reason ISSUE_NOT_DISCARDED) si
	// no está en un estado CANCELED.
	Restore(ctx context.Context, projectID string, number int32, author model.Author) (*model.Issue, error)
	// AddComment guarda el comentario (ID, AuthorUID y Body ya seteados) en
	// el issue y su evento.
	AddComment(ctx context.Context, projectID string, number int32, comment *model.Comment, author model.Author) (*model.Comment, error)
	// ListEvents pagina el historial del issue, del más viejo al más nuevo.
	ListEvents(ctx context.Context, projectID string, number int32, page PageFilter) (*EventPage, error)
	// Import crea o actualiza el issue de spec.SourceKey (ver
	// model.ImportSpec) con el lock del proyecto, que tiene que ser de uno de
	// workspaceIDs (si no, NotFound). Al crear: Conflict (reason
	// ISSUE_NUMBER_TAKEN) si el número ya es de otro issue; next_issue_number
	// queda mayor que el número importado. Al actualizar: FailedPrecondition
	// (reason IMPORT_NUMBER_MISMATCH) si el número no coincide. Cada cambio
	// registra su evento como en los RPC sueltos.
	Import(ctx context.Context, workspaceIDs []string, spec model.ImportSpec, author model.Author) (*model.Issue, model.ImportOutcome, error)
}

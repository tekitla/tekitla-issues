package port

import (
	"context"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// IssueFilter parametriza ListIssues. State vacío = todos los estados.
type IssueFilter struct {
	ProjectID string
	State     model.IssueState
	Cursor    string
	Limit     int32
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
//   - cualquier otra cosa    -> domainerrors.InternalError
type IssueRepository interface {
	// Create asigna el siguiente número del proyecto y un rank al final del
	// orden (seguro con concurrencia), inserta el issue (StatusID y State ya
	// seteados) y su evento «creado» en la misma transacción. Si el proyecto
	// no existe o no es de uno de workspaceIDs devuelve NotFound. Devuelve el
	// issue con Number, Rank y timestamps seteados.
	Create(ctx context.Context, workspaceIDs []string, issue *model.Issue, actorUID string) (*model.Issue, error)
	// GetByNumber busca un issue por su número dentro del proyecto.
	GetByNumber(ctx context.Context, projectID string, number int32) (*model.Issue, error)
	// List pagina los issues del proyecto por rank ASC.
	List(ctx context.Context, filter IssueFilter) (*IssuePage, error)
	// Move pasa el issue al estado target (del mismo proyecto) y, si place no
	// es cero, le calcula un rank entre sus vecinos. Registra el cambio de
	// estado (reordenar no genera evento). Sin cambios, devuelve el issue tal
	// cual.
	Move(ctx context.Context, projectID string, number int32, target *model.Status, place Placement, actorUID string) (*model.Issue, error)
	// Update aplica el patch (ya validado; las etiquetas son del proyecto) y
	// registra un evento por cada campo que de verdad cambia.
	Update(ctx context.Context, projectID string, number int32, patch model.IssuePatch, actorUID string) (*model.Issue, error)
	// AddComment guarda el comentario (ID, AuthorUID y Body ya seteados) en
	// el issue y su evento.
	AddComment(ctx context.Context, projectID string, number int32, comment *model.Comment) (*model.Comment, error)
	// ListEvents pagina el historial del issue, del más viejo al más nuevo.
	ListEvents(ctx context.Context, projectID string, number int32, page PageFilter) (*EventPage, error)
}

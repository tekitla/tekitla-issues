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

// IssueRepository define la persistencia de Issue.
//
// La pertenencia del proyecto a los espacios del usuario la verifica el
// service (con ProjectRepository.GetByID) antes de llamar a estos métodos;
// Create la vuelve a exigir porque reserva el número sobre la fila del proyecto.
//
// Contrato de errores (todas las implementaciones):
//   - no existe           -> domainerrors.NotFoundError
//   - unique violation    -> domainerrors.ConflictError
//   - cursor inválido     -> domainerrors.ValidationError
//   - cualquier otra cosa -> domainerrors.InternalError
type IssueRepository interface {
	// Create asigna el siguiente número del proyecto y un rank al final del
	// orden (seguro con concurrencia) e inserta el issue en la misma
	// transacción. Si el proyecto no existe o no es de uno de workspaceIDs
	// devuelve NotFound. Devuelve el issue con Number, Rank y timestamps
	// seteados.
	Create(ctx context.Context, workspaceIDs []string, issue *model.Issue) (*model.Issue, error)
	// GetByNumber busca un issue por su número dentro del proyecto.
	GetByNumber(ctx context.Context, projectID string, number int32) (*model.Issue, error)
	// List pagina los issues del proyecto por rank ASC (id ASC como desempate).
	List(ctx context.Context, filter IssueFilter) (*IssuePage, error)
	// UpdateState cambia el estado y devuelve el issue actualizado.
	UpdateState(ctx context.Context, projectID string, number int32, state model.IssueState) (*model.Issue, error)
}

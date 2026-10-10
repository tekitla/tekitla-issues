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
// La pertenencia del proyecto al usuario la verifica el service (con
// ProjectRepository.GetByID) antes de llamar a estos métodos; Create la
// vuelve a exigir porque reserva el número sobre la fila del proyecto.
//
// Contrato de errores (todas las implementaciones):
//   - no existe           -> domainerrors.NotFoundError
//   - unique violation    -> domainerrors.ConflictError
//   - cursor inválido     -> domainerrors.ValidationError
//   - cualquier otra cosa -> domainerrors.InternalError
type IssueRepository interface {
	// Create asigna el siguiente número del proyecto (seguro con concurrencia)
	// e inserta el issue en la misma transacción. Si el proyecto no existe o
	// no es de ownerUID devuelve NotFound. Devuelve el issue con Number y
	// timestamps seteados.
	Create(ctx context.Context, ownerUID string, issue *model.Issue) (*model.Issue, error)
	// GetByNumber busca un issue por su número dentro del proyecto.
	GetByNumber(ctx context.Context, projectID string, number int32) (*model.Issue, error)
	// List pagina los issues del proyecto: prioridad DESC, created_at DESC, id DESC.
	List(ctx context.Context, filter IssueFilter) (*IssuePage, error)
	// UpdateState cambia el estado y devuelve el issue actualizado.
	UpdateState(ctx context.Context, projectID string, number int32, state model.IssueState) (*model.Issue, error)
}

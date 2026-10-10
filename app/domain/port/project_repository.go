package port

import (
	"context"

	"github.com/vrojas1015/tequio-issues/app/domain/model"
)

// PageFilter parametriza una lista paginada.
// Cursor es opaco (lo genera el repo); Limit ya llega normalizado por el service.
type PageFilter struct {
	Cursor string
	Limit  int32
}

// ProjectPage es una página de proyectos con cursor keyset.
type ProjectPage struct {
	Projects   []*model.Project
	NextCursor string
	HasNext    bool
}

// ProjectRepository define la persistencia de Project.
//
// Todas las operaciones se acotan a un dueño (ownerUID): un proyecto de otro
// dueño se comporta exactamente igual que uno que no existe (NotFound).
//
// Contrato de errores (todas las implementaciones):
//   - no existe / es ajeno -> domainerrors.NotFoundError
//   - key duplicada        -> domainerrors.ConflictError
//   - cursor inválido      -> domainerrors.ValidationError
//   - cualquier otra cosa  -> domainerrors.InternalError
type ProjectRepository interface {
	// Create inserta el proyecto (ID, OwnerUID y Key ya seteados).
	Create(ctx context.Context, project *model.Project) (*model.Project, error)
	// GetByID devuelve el proyecto si pertenece a ownerUID.
	GetByID(ctx context.Context, ownerUID, id string) (*model.Project, error)
	// List pagina los proyectos de ownerUID ordenados por nombre (y por id
	// como desempate estable).
	List(ctx context.Context, ownerUID string, filter PageFilter) (*ProjectPage, error)
}

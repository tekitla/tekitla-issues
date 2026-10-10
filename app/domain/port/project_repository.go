package port

import (
	"context"

	"github.com/tekitla/tekitla-issues/app/domain/model"
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
// Las lecturas se acotan a los espacios de trabajo del usuario (workspaceIDs,
// que resuelve el service): un proyecto de otro espacio se comporta
// exactamente igual que uno que no existe (NotFound).
//
// Contrato de errores (todas las implementaciones):
//   - no existe / es ajeno -> domainerrors.NotFoundError
//   - key duplicada        -> domainerrors.ConflictError
//   - cursor inválido      -> domainerrors.ValidationError
//   - cualquier otra cosa  -> domainerrors.InternalError
type ProjectRepository interface {
	// Create inserta el proyecto (ID, WorkspaceID y Key ya seteados).
	Create(ctx context.Context, project *model.Project) (*model.Project, error)
	// GetByID devuelve el proyecto si pertenece a uno de workspaceIDs
	// (workspaceIDs vacío -> NotFound).
	GetByID(ctx context.Context, workspaceIDs []string, id string) (*model.Project, error)
	// List pagina los proyectos de workspaceIDs ordenados por nombre (y por id
	// como desempate estable). workspaceIDs vacío -> página vacía, pero el
	// cursor se valida igual.
	List(ctx context.Context, workspaceIDs []string, filter PageFilter) (*ProjectPage, error)
}

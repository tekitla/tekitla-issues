package port

import (
	"context"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// WorkspaceRepository define la persistencia de Workspace.
//
// En la rebanada 1 solo existe el espacio personal de cada usuario. Desde la
// rebanada 3 los espacios de equipo los resuelve el gateway con
// accounts-service (issue 005) y este port cambia de forma.
//
// Contrato de errores (todas las implementaciones):
//   - GetPersonal sin espacio -> domainerrors.NotFoundError
//   - cualquier otra cosa     -> domainerrors.InternalError
type WorkspaceRepository interface {
	// GetPersonal devuelve el espacio personal de ownerUID. Es de solo
	// lectura: un usuario que nunca creó un proyecto no tiene espacio.
	GetPersonal(ctx context.Context, ownerUID string) (*model.Workspace, error)
	// EnsurePersonal devuelve el espacio personal de ws.OwnerUID; si no existe
	// lo crea con ws.ID (seguro con concurrencia: dos primeros accesos
	// simultáneos terminan con el mismo espacio).
	EnsurePersonal(ctx context.Context, ws *model.Workspace) (*model.Workspace, error)
}

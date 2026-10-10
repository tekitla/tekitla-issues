package application

import (
	"context"
	"slices"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// workspaces resuelve los espacios de trabajo de un usuario.
//
// En la rebanada 1 cada usuario tiene solo su espacio personal, que se crea
// con su primer proyecto (las lecturas no escriben). Desde la rebanada 3 la
// lista llega del gateway (tenants de accounts-service, issue 005) y esto deja
// de tocar la base.
type workspaces struct {
	repo port.WorkspaceRepository
}

// ensurePersonal devuelve el espacio personal de ownerUID y lo crea si no
// existe. Solo para escrituras (CreateProject).
func (w workspaces) ensurePersonal(ctx context.Context, ownerUID string) (*model.Workspace, error) {
	return w.repo.EnsurePersonal(ctx, &model.Workspace{ID: uuid.NewString(), OwnerUID: ownerUID})
}

// ids devuelve los ids de los espacios de ownerUID, sin crear nada: un usuario
// sin espacio todavía tiene la lista vacía.
func (w workspaces) ids(ctx context.Context, ownerUID string) ([]string, error) {
	ws, err := w.repo.GetPersonal(ctx, ownerUID)
	if domainerrors.IsNotFound(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return []string{ws.ID}, nil
}

// visible acota ids a workspaceID (vacío = todos). Un espacio que no es del
// usuario da una lista vacía, igual que uno que no existe.
func visible(ids []string, workspaceID string) []string {
	if workspaceID == "" {
		return ids
	}
	if slices.Contains(ids, workspaceID) {
		return []string{workspaceID}
	}
	return []string{}
}

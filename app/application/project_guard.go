package application

import (
	"context"

	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// projectGuard verifica que un proyecto sea de uno de los espacios del
// usuario antes de tocar algo suyo. Lo comparten IssueService, StatusService
// y LabelService.
type projectGuard struct {
	workspaces workspaces
	projects   port.ProjectRepository
}

// ensure valida el usuario y el project_id y verifica que el proyecto sea de
// uno de sus espacios (NotFound si no existe o es ajeno, para no revelar que
// existe). Devuelve los espacios del usuario.
func (g projectGuard) ensure(ctx context.Context, ownerUID, projectID string) ([]string, error) {
	if err := requireOwner(ownerUID); err != nil {
		return nil, err
	}
	if err := validateUUID("project_id", projectID); err != nil {
		return nil, err
	}
	workspaceIDs, err := g.workspaces.ids(ctx, ownerUID)
	if err != nil {
		return nil, err
	}
	if _, err := g.projects.GetByID(ctx, workspaceIDs, projectID); err != nil {
		return nil, err
	}
	return workspaceIDs, nil
}

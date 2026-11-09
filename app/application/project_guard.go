package application

import (
	"context"

	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// projectGuard verifica que un proyecto sea de uno de los espacios del
// usuario antes de tocar algo suyo. Lo comparten IssueService, StatusService
// y LabelService.
type projectGuard struct {
	workspaces workspaces
	projects   port.ProjectRepository
	// actors verifica el agente de las escrituras (ensureAuthor).
	actors actorAccess
}

// ensureAuthor es ensure para una escritura: además, si la hace un agente,
// verifica que sea un agente activo de la persona (defensa en profundidad:
// el gateway ya lo resolvió, pero su caché puede tener hasta 60 s).
func (g projectGuard) ensureAuthor(ctx context.Context, who model.Author, projectID string) ([]string, error) {
	workspaceIDs, err := g.ensure(ctx, who.UserID, projectID)
	if err != nil {
		return nil, err
	}
	if err := g.actors.activeAgent(ctx, who); err != nil {
		return nil, err
	}
	return workspaceIDs, nil
}

// ensure valida el usuario y el project_id y verifica que el proyecto sea de
// uno de sus espacios (NotFound si no existe o es ajeno, para no revelar que
// existe). Devuelve los espacios del usuario.
func (g projectGuard) ensure(ctx context.Context, ownerUID, projectID string) ([]string, error) {
	_, workspaceIDs, err := g.project(ctx, ownerUID, projectID)
	return workspaceIDs, err
}

// project es ensure que además devuelve el proyecto.
func (g projectGuard) project(ctx context.Context, ownerUID, projectID string) (*model.Project, []string, error) {
	if err := requireOwner(ownerUID); err != nil {
		return nil, nil, err
	}
	if err := validateUUID("project_id", projectID); err != nil {
		return nil, nil, err
	}
	workspaceIDs, err := g.workspaces.ids(ctx, ownerUID)
	if err != nil {
		return nil, nil, err
	}
	project, err := g.projects.GetByID(ctx, workspaceIDs, projectID)
	if err != nil {
		return nil, nil, err
	}
	return project, workspaceIDs, nil
}

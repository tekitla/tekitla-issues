package application

import (
	"context"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// ProjectService implementa los casos de uso de Project.
//
// Reglas:
//   - Solo depende de ports, nunca de infra ni de proto.
//   - El usuario (ownerUID) llega como parámetro explícito desde el adapter;
//     sus espacios de trabajo los resuelve workspaces.
//   - Los errores del repo se propagan tal cual (ya son DomainError).
type ProjectService struct {
	workspaces workspaces
	repo       port.ProjectRepository
}

// NewProjectService construye el servicio.
func NewProjectService(workspaceRepo port.WorkspaceRepository, repo port.ProjectRepository) *ProjectService {
	return &ProjectService{workspaces: workspaces{repo: workspaceRepo}, repo: repo}
}

// CreateProjectInput son los datos que acepta CreateProject.
// WorkspaceID vacío = el espacio personal del usuario.
type CreateProjectInput struct {
	WorkspaceID string
	Key         string
	Name        string
	Description string
}

// ListProjectsInput son los datos que acepta ListProjects.
// WorkspaceID vacío = todos los espacios del usuario.
type ListProjectsInput struct {
	WorkspaceID string
	Page        port.PageFilter
}

// CreateProject valida y persiste un proyecto nuevo en un espacio de ownerUID.
// Key duplicada en el mismo espacio -> ConflictError (ALREADY_EXISTS); un
// espacio que no es del usuario -> NotFoundError.
func (s *ProjectService) CreateProject(ctx context.Context, ownerUID string, in CreateProjectInput) (*model.Project, error) {
	if err := requireOwner(ownerUID); err != nil {
		return nil, err
	}
	key, err := model.NormalizeProjectKey(in.Key)
	if err != nil {
		return nil, err
	}
	name, err := model.NormalizeProjectName(in.Name)
	if err != nil {
		return nil, err
	}
	desc, err := model.NormalizeProjectDescription(in.Description)
	if err != nil {
		return nil, err
	}

	workspaceID, err := s.targetWorkspace(ctx, ownerUID, in.WorkspaceID)
	if err != nil {
		return nil, err
	}

	project := &model.Project{
		ID:          uuid.NewString(),
		WorkspaceID: workspaceID,
		Key:         key,
		Name:        name,
		Description: desc,
	}
	statuses := make([]*model.Status, len(model.DefaultStatuses))
	for i, d := range model.DefaultStatuses {
		statuses[i] = &model.Status{ID: uuid.NewString(), Name: d.Name, Category: d.Category}
	}
	created, err := s.repo.Create(ctx, project, statuses)
	if err != nil {
		if domainerrors.IsConflict(err) {
			return nil, domainerrors.ConflictError("project key already exists", map[string]any{"key": key}).
				WithReason("PROJECT_KEY_TAKEN")
		}
		return nil, err
	}
	return created, nil
}

// ListProjects devuelve una página de los proyectos de los espacios de
// ownerUID (o de uno, con WorkspaceID), por nombre. Un espacio ajeno da una
// página vacía, no un error (el repo igual valida el cursor). limit <= 0 usa
// DefaultPageLimit; limit > MaxPageLimit se recorta.
func (s *ProjectService) ListProjects(ctx context.Context, ownerUID string, in ListProjectsInput) (*port.ProjectPage, error) {
	if err := requireOwner(ownerUID); err != nil {
		return nil, err
	}
	ids, err := s.workspaces.ids(ctx, ownerUID)
	if err != nil {
		return nil, err
	}
	in.Page.Limit = normalizeLimit(in.Page.Limit)
	return s.repo.List(ctx, visible(ids, in.WorkspaceID), in.Page)
}

// targetWorkspace resuelve el espacio donde se crea un proyecto: el personal
// si workspaceID es vacío; si no, tiene que ser uno de los del usuario.
func (s *ProjectService) targetWorkspace(ctx context.Context, ownerUID, workspaceID string) (string, error) {
	if workspaceID == "" {
		ws, err := s.workspaces.ensurePersonal(ctx, ownerUID)
		if err != nil {
			return "", err
		}
		return ws.ID, nil
	}
	ids, err := s.workspaces.ids(ctx, ownerUID)
	if err != nil {
		return "", err
	}
	if len(visible(ids, workspaceID)) == 0 {
		return "", domainerrors.NotFoundError("workspace not found", map[string]any{"workspace_id": workspaceID})
	}
	return workspaceID, nil
}

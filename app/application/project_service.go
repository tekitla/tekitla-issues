package application

import (
	"context"

	"github.com/google/uuid"

	domainerrors "github.com/vrojas1015/tequio-issues/app/domain/errors"
	"github.com/vrojas1015/tequio-issues/app/domain/model"
	"github.com/vrojas1015/tequio-issues/app/domain/port"
)

// ProjectService implementa los casos de uso de Project.
//
// Reglas:
//   - Solo depende de ports, nunca de infra ni de proto.
//   - El dueño (ownerUID) llega como parámetro explícito desde el adapter.
//   - Los errores del repo se propagan tal cual (ya son DomainError).
type ProjectService struct {
	repo port.ProjectRepository
}

// NewProjectService construye el servicio.
func NewProjectService(repo port.ProjectRepository) *ProjectService {
	return &ProjectService{repo: repo}
}

// CreateProjectInput son los datos que acepta CreateProject.
type CreateProjectInput struct {
	Key         string
	Name        string
	Description string
}

// CreateProject valida y persiste un proyecto nuevo de ownerUID.
// Key duplicada para el mismo dueño -> ConflictError (ALREADY_EXISTS).
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

	project := &model.Project{
		ID:          uuid.NewString(),
		OwnerUID:    ownerUID,
		Key:         key,
		Name:        name,
		Description: desc,
	}
	created, err := s.repo.Create(ctx, project)
	if err != nil {
		if domainerrors.IsConflict(err) {
			return nil, domainerrors.ConflictError("project key already exists", map[string]any{"key": key}).
				WithReason("PROJECT_KEY_TAKEN")
		}
		return nil, err
	}
	return created, nil
}

// ListProjects devuelve una página de los proyectos de ownerUID, por nombre.
// limit <= 0 usa DefaultPageLimit; limit > MaxPageLimit se recorta.
func (s *ProjectService) ListProjects(ctx context.Context, ownerUID string, filter port.PageFilter) (*port.ProjectPage, error) {
	if err := requireOwner(ownerUID); err != nil {
		return nil, err
	}
	filter.Limit = normalizeLimit(filter.Limit)
	return s.repo.List(ctx, ownerUID, filter)
}

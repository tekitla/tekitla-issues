package application

import (
	"context"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// StatusService implementa los casos de uso de los estados de un proyecto
// (las columnas del tablero).
type StatusService struct {
	guard    projectGuard
	statuses port.StatusRepository
}

// NewStatusService construye el servicio.
func NewStatusService(workspaceRepo port.WorkspaceRepository, projects port.ProjectRepository, statuses port.StatusRepository) *StatusService {
	return &StatusService{
		guard:    projectGuard{workspaces: workspaces{repo: workspaceRepo}, projects: projects},
		statuses: statuses,
	}
}

// CreateStatusInput son los datos que acepta CreateStatus. AfterStatusID
// vacío = al final.
type CreateStatusInput struct {
	ProjectID     string
	Name          string
	Category      model.IssueState
	AfterStatusID string
}

// UpdateStatusInput son los datos que acepta UpdateStatus: nil = no cambia.
// AfterStatusID presente y vacío = al principio.
type UpdateStatusInput struct {
	ProjectID     string
	StatusID      string
	Name          *string
	AfterStatusID *string
}

// ListStatuses devuelve los estados del proyecto por rank.
func (s *StatusService) ListStatuses(ctx context.Context, ownerUID, projectID string) ([]*model.Status, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, projectID); err != nil {
		return nil, err
	}
	return s.statuses.List(ctx, projectID)
}

// CreateStatus agrega un estado al proyecto.
func (s *StatusService) CreateStatus(ctx context.Context, ownerUID string, in CreateStatusInput) (*model.Status, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, in.ProjectID); err != nil {
		return nil, err
	}
	name, err := model.NormalizeStatusName(in.Name)
	if err != nil {
		return nil, err
	}
	if !in.Category.IsValid() {
		if in.Category == model.IssueStateUnspecified {
			return nil, domainerrors.ValidationError("category is required", map[string]any{"field": "category"})
		}
		return nil, domainerrors.ValidationError("invalid category", map[string]any{"field": "category", "value": string(in.Category)})
	}
	if in.AfterStatusID != "" {
		if err := validateUUID("after_status_id", in.AfterStatusID); err != nil {
			return nil, err
		}
	}
	status := &model.Status{
		ID:        uuid.NewString(),
		ProjectID: in.ProjectID,
		Name:      name,
		Category:  in.Category,
	}
	return s.statuses.Create(ctx, status, in.AfterStatusID)
}

// UpdateStatus renombra o reubica un estado. Sin cambios pedidos lo devuelve
// tal cual.
func (s *StatusService) UpdateStatus(ctx context.Context, ownerUID string, in UpdateStatusInput) (*model.Status, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, in.ProjectID); err != nil {
		return nil, err
	}
	if err := validateUUID("status_id", in.StatusID); err != nil {
		return nil, err
	}
	var name *string
	if in.Name != nil {
		n, err := model.NormalizeStatusName(*in.Name)
		if err != nil {
			return nil, err
		}
		name = &n
	}
	if in.AfterStatusID != nil && *in.AfterStatusID != "" {
		if err := validateUUID("after_status_id", *in.AfterStatusID); err != nil {
			return nil, err
		}
		if *in.AfterStatusID == in.StatusID {
			return nil, domainerrors.ValidationError("after_status_id must be another status", map[string]any{"field": "after_status_id"})
		}
	}
	if name == nil && in.AfterStatusID == nil {
		return s.statuses.Get(ctx, in.ProjectID, in.StatusID)
	}
	return s.statuses.Update(ctx, in.ProjectID, in.StatusID, name, in.AfterStatusID)
}

// DeleteStatus borra un estado y pasa sus issues a moveToStatusID.
func (s *StatusService) DeleteStatus(ctx context.Context, ownerUID, projectID, statusID, moveToStatusID string) error {
	if _, err := s.guard.ensure(ctx, ownerUID, projectID); err != nil {
		return err
	}
	if err := validateUUID("status_id", statusID); err != nil {
		return err
	}
	if moveToStatusID != "" {
		if err := validateUUID("move_to_status_id", moveToStatusID); err != nil {
			return err
		}
		if moveToStatusID == statusID {
			return domainerrors.ValidationError("move_to_status_id must be another status", map[string]any{"field": "move_to_status_id"})
		}
	}
	return s.statuses.Delete(ctx, projectID, statusID, moveToStatusID, ownerUID)
}

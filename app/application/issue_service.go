package application

import (
	"context"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// IssueService implementa los casos de uso de Issue.
//
// Todo caso de uso verifica primero que el proyecto sea de ownerUID: un
// proyecto ajeno responde NotFound, igual que uno inexistente, para no revelar
// que existe.
type IssueService struct {
	projects port.ProjectRepository
	issues   port.IssueRepository
}

// NewIssueService construye el servicio.
func NewIssueService(projects port.ProjectRepository, issues port.IssueRepository) *IssueService {
	return &IssueService{projects: projects, issues: issues}
}

// ListIssuesInput son los datos que acepta ListIssues. State vacío = todos.
type ListIssuesInput struct {
	ProjectID string
	State     model.IssueState
	Cursor    string
	Limit     int32
}

// CreateIssueInput son los datos que acepta CreateIssue.
// Priority sin especificar se guarda como model.DefaultPriority.
type CreateIssueInput struct {
	ProjectID   string
	Title       string
	Description string
	Priority    model.Priority
}

// MoveIssueInput son los datos que acepta MoveIssue.
type MoveIssueInput struct {
	ProjectID string
	Number    int32
	State     model.IssueState
}

// ListIssues devuelve una página de issues del proyecto: prioridad
// descendente y, dentro de la misma, el más reciente primero.
func (s *IssueService) ListIssues(ctx context.Context, ownerUID string, in ListIssuesInput) (*port.IssuePage, error) {
	if err := s.ensureProject(ctx, ownerUID, in.ProjectID); err != nil {
		return nil, err
	}
	if in.State != model.IssueStateUnspecified && !in.State.IsValid() {
		return nil, invalidState(in.State)
	}
	return s.issues.List(ctx, port.IssueFilter{
		ProjectID: in.ProjectID,
		State:     in.State,
		Cursor:    in.Cursor,
		Limit:     normalizeLimit(in.Limit),
	})
}

// GetIssue devuelve un issue por su número dentro del proyecto.
func (s *IssueService) GetIssue(ctx context.Context, ownerUID, projectID string, number int32) (*model.Issue, error) {
	if err := s.ensureProject(ctx, ownerUID, projectID); err != nil {
		return nil, err
	}
	if err := validateIssueNumber(number); err != nil {
		return nil, err
	}
	return s.issues.GetByNumber(ctx, projectID, number)
}

// CreateIssue crea un issue en estado Todo con el siguiente número del proyecto.
func (s *IssueService) CreateIssue(ctx context.Context, ownerUID string, in CreateIssueInput) (*model.Issue, error) {
	if err := s.ensureProject(ctx, ownerUID, in.ProjectID); err != nil {
		return nil, err
	}
	title, err := model.NormalizeIssueTitle(in.Title)
	if err != nil {
		return nil, err
	}
	desc, err := model.NormalizeIssueDescription(in.Description)
	if err != nil {
		return nil, err
	}
	priority := in.Priority
	if priority == model.PriorityUnspecified {
		priority = model.DefaultPriority
	}
	if !priority.IsValid() {
		return nil, domainerrors.ValidationError("invalid priority", map[string]any{"field": "priority", "value": int(in.Priority)})
	}

	issue := &model.Issue{
		ID:          uuid.NewString(),
		ProjectID:   in.ProjectID,
		Title:       title,
		Description: desc,
		State:       model.IssueStateTodo,
		Priority:    priority,
	}
	return s.issues.Create(ctx, ownerUID, issue)
}

// MoveIssue cambia el estado de un issue. Estado vacío o desconocido -> ValidationError.
func (s *IssueService) MoveIssue(ctx context.Context, ownerUID string, in MoveIssueInput) (*model.Issue, error) {
	if err := s.ensureProject(ctx, ownerUID, in.ProjectID); err != nil {
		return nil, err
	}
	if err := validateIssueNumber(in.Number); err != nil {
		return nil, err
	}
	if !in.State.IsValid() {
		return nil, invalidState(in.State)
	}
	return s.issues.UpdateState(ctx, in.ProjectID, in.Number, in.State)
}

// ensureProject valida el dueño y el project_id y verifica que el proyecto
// sea de ownerUID (NotFound si no existe o es ajeno).
func (s *IssueService) ensureProject(ctx context.Context, ownerUID, projectID string) error {
	if err := requireOwner(ownerUID); err != nil {
		return err
	}
	if err := validateUUID("project_id", projectID); err != nil {
		return err
	}
	_, err := s.projects.GetByID(ctx, ownerUID, projectID)
	return err
}

func invalidState(state model.IssueState) error {
	if state == model.IssueStateUnspecified {
		return domainerrors.ValidationError("state is required", map[string]any{"field": "state"})
	}
	return domainerrors.ValidationError("invalid state", map[string]any{"field": "state", "value": string(state)})
}

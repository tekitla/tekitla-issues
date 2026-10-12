package application_test

import (
	"context"

	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// Mocks compartidos por los tests del paquete application.
//
// Patrón: struct con campos func opcionales. Si un campo no está seteado, el
// método devuelve zero values. Nada de testify/gomock/mockery: solo stdlib.

// mockWorkspaceRepo satisface port.WorkspaceRepository.
type mockWorkspaceRepo struct {
	getPersonalFn    func(ctx context.Context, ownerUID string) (*model.Workspace, error)
	ensurePersonalFn func(ctx context.Context, ws *model.Workspace) (*model.Workspace, error)
}

var _ port.WorkspaceRepository = (*mockWorkspaceRepo)(nil)

func (m *mockWorkspaceRepo) GetPersonal(ctx context.Context, ownerUID string) (*model.Workspace, error) {
	if m.getPersonalFn == nil {
		return nil, nil
	}
	return m.getPersonalFn(ctx, ownerUID)
}

func (m *mockWorkspaceRepo) EnsurePersonal(ctx context.Context, ws *model.Workspace) (*model.Workspace, error) {
	if m.ensurePersonalFn == nil {
		return nil, nil
	}
	return m.ensurePersonalFn(ctx, ws)
}

// mockProjectRepo satisface port.ProjectRepository.
type mockProjectRepo struct {
	createFn  func(ctx context.Context, p *model.Project, statuses []*model.Status) (*model.Project, error)
	getByIDFn func(ctx context.Context, workspaceIDs []string, id string) (*model.Project, error)
	listFn    func(ctx context.Context, workspaceIDs []string, f port.PageFilter) (*port.ProjectPage, error)
}

var _ port.ProjectRepository = (*mockProjectRepo)(nil)

func (m *mockProjectRepo) Create(ctx context.Context, p *model.Project, statuses []*model.Status) (*model.Project, error) {
	if m.createFn == nil {
		return nil, nil
	}
	return m.createFn(ctx, p, statuses)
}

func (m *mockProjectRepo) GetByID(ctx context.Context, workspaceIDs []string, id string) (*model.Project, error) {
	if m.getByIDFn == nil {
		return nil, nil
	}
	return m.getByIDFn(ctx, workspaceIDs, id)
}

func (m *mockProjectRepo) List(ctx context.Context, workspaceIDs []string, f port.PageFilter) (*port.ProjectPage, error) {
	if m.listFn == nil {
		return nil, nil
	}
	return m.listFn(ctx, workspaceIDs, f)
}

// mockIssueRepo satisface port.IssueRepository.
type mockIssueRepo struct {
	createFn      func(ctx context.Context, workspaceIDs []string, issue *model.Issue, actorUID string) (*model.Issue, error)
	getByNumberFn func(ctx context.Context, projectID string, number int32) (*model.Issue, error)
	listFn        func(ctx context.Context, f port.IssueFilter) (*port.IssuePage, error)
	moveFn        func(ctx context.Context, projectID string, number int32, target *model.Status, place port.Placement, actorUID string) (*model.Issue, error)
	updateFn      func(ctx context.Context, projectID string, number int32, patch model.IssuePatch, actorUID string) (*model.Issue, error)
	addCommentFn  func(ctx context.Context, projectID string, number int32, c *model.Comment) (*model.Comment, error)
	listEventsFn  func(ctx context.Context, projectID string, number int32, page port.PageFilter) (*port.EventPage, error)
}

var _ port.IssueRepository = (*mockIssueRepo)(nil)

func (m *mockIssueRepo) Create(ctx context.Context, workspaceIDs []string, issue *model.Issue, actorUID string) (*model.Issue, error) {
	if m.createFn == nil {
		return nil, nil
	}
	return m.createFn(ctx, workspaceIDs, issue, actorUID)
}

func (m *mockIssueRepo) GetByNumber(ctx context.Context, projectID string, number int32) (*model.Issue, error) {
	if m.getByNumberFn == nil {
		return nil, nil
	}
	return m.getByNumberFn(ctx, projectID, number)
}

func (m *mockIssueRepo) List(ctx context.Context, f port.IssueFilter) (*port.IssuePage, error) {
	if m.listFn == nil {
		return nil, nil
	}
	return m.listFn(ctx, f)
}

func (m *mockIssueRepo) Move(ctx context.Context, projectID string, number int32, target *model.Status, place port.Placement, actorUID string) (*model.Issue, error) {
	if m.moveFn == nil {
		return nil, nil
	}
	return m.moveFn(ctx, projectID, number, target, place, actorUID)
}

func (m *mockIssueRepo) Update(ctx context.Context, projectID string, number int32, patch model.IssuePatch, actorUID string) (*model.Issue, error) {
	if m.updateFn == nil {
		return nil, nil
	}
	return m.updateFn(ctx, projectID, number, patch, actorUID)
}

func (m *mockIssueRepo) AddComment(ctx context.Context, projectID string, number int32, c *model.Comment) (*model.Comment, error) {
	if m.addCommentFn == nil {
		return nil, nil
	}
	return m.addCommentFn(ctx, projectID, number, c)
}

func (m *mockIssueRepo) ListEvents(ctx context.Context, projectID string, number int32, page port.PageFilter) (*port.EventPage, error) {
	if m.listEventsFn == nil {
		return nil, nil
	}
	return m.listEventsFn(ctx, projectID, number, page)
}

// mockStatusRepo satisface port.StatusRepository.
type mockStatusRepo struct {
	listFn   func(ctx context.Context, projectID string) ([]*model.Status, error)
	getFn    func(ctx context.Context, projectID, id string) (*model.Status, error)
	createFn func(ctx context.Context, s *model.Status, afterID string) (*model.Status, error)
	updateFn func(ctx context.Context, projectID, id string, name, afterID *string) (*model.Status, error)
	deleteFn func(ctx context.Context, projectID, id, moveToID, actorUID string) error
}

var _ port.StatusRepository = (*mockStatusRepo)(nil)

func (m *mockStatusRepo) List(ctx context.Context, projectID string) ([]*model.Status, error) {
	if m.listFn == nil {
		return nil, nil
	}
	return m.listFn(ctx, projectID)
}

func (m *mockStatusRepo) Get(ctx context.Context, projectID, id string) (*model.Status, error) {
	if m.getFn == nil {
		return nil, nil
	}
	return m.getFn(ctx, projectID, id)
}

func (m *mockStatusRepo) Create(ctx context.Context, s *model.Status, afterID string) (*model.Status, error) {
	if m.createFn == nil {
		return nil, nil
	}
	return m.createFn(ctx, s, afterID)
}

func (m *mockStatusRepo) Update(ctx context.Context, projectID, id string, name, afterID *string) (*model.Status, error) {
	if m.updateFn == nil {
		return nil, nil
	}
	return m.updateFn(ctx, projectID, id, name, afterID)
}

func (m *mockStatusRepo) Delete(ctx context.Context, projectID, id, moveToID, actorUID string) error {
	if m.deleteFn == nil {
		return nil
	}
	return m.deleteFn(ctx, projectID, id, moveToID, actorUID)
}

// mockLabelRepo satisface port.LabelRepository.
type mockLabelRepo struct {
	listFn   func(ctx context.Context, projectID string) ([]*model.Label, error)
	createFn func(ctx context.Context, l *model.Label) (*model.Label, error)
	updateFn func(ctx context.Context, projectID, id string, name *string, color *model.LabelColor) (*model.Label, error)
	deleteFn func(ctx context.Context, projectID, id string) error
}

var _ port.LabelRepository = (*mockLabelRepo)(nil)

func (m *mockLabelRepo) List(ctx context.Context, projectID string) ([]*model.Label, error) {
	if m.listFn == nil {
		return nil, nil
	}
	return m.listFn(ctx, projectID)
}

func (m *mockLabelRepo) Create(ctx context.Context, l *model.Label) (*model.Label, error) {
	if m.createFn == nil {
		return nil, nil
	}
	return m.createFn(ctx, l)
}

func (m *mockLabelRepo) Update(ctx context.Context, projectID, id string, name *string, color *model.LabelColor) (*model.Label, error) {
	if m.updateFn == nil {
		return nil, nil
	}
	return m.updateFn(ctx, projectID, id, name, color)
}

func (m *mockLabelRepo) Delete(ctx context.Context, projectID, id string) error {
	if m.deleteFn == nil {
		return nil
	}
	return m.deleteFn(ctx, projectID, id)
}

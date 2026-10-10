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

// mockProjectRepo satisface port.ProjectRepository.
type mockProjectRepo struct {
	createFn  func(ctx context.Context, p *model.Project) (*model.Project, error)
	getByIDFn func(ctx context.Context, ownerUID, id string) (*model.Project, error)
	listFn    func(ctx context.Context, ownerUID string, f port.PageFilter) (*port.ProjectPage, error)
}

var _ port.ProjectRepository = (*mockProjectRepo)(nil)

func (m *mockProjectRepo) Create(ctx context.Context, p *model.Project) (*model.Project, error) {
	if m.createFn == nil {
		return nil, nil
	}
	return m.createFn(ctx, p)
}

func (m *mockProjectRepo) GetByID(ctx context.Context, ownerUID, id string) (*model.Project, error) {
	if m.getByIDFn == nil {
		return nil, nil
	}
	return m.getByIDFn(ctx, ownerUID, id)
}

func (m *mockProjectRepo) List(ctx context.Context, ownerUID string, f port.PageFilter) (*port.ProjectPage, error) {
	if m.listFn == nil {
		return nil, nil
	}
	return m.listFn(ctx, ownerUID, f)
}

// mockIssueRepo satisface port.IssueRepository.
type mockIssueRepo struct {
	createFn      func(ctx context.Context, ownerUID string, issue *model.Issue) (*model.Issue, error)
	getByNumberFn func(ctx context.Context, projectID string, number int32) (*model.Issue, error)
	listFn        func(ctx context.Context, f port.IssueFilter) (*port.IssuePage, error)
	updateStateFn func(ctx context.Context, projectID string, number int32, state model.IssueState) (*model.Issue, error)
}

var _ port.IssueRepository = (*mockIssueRepo)(nil)

func (m *mockIssueRepo) Create(ctx context.Context, ownerUID string, issue *model.Issue) (*model.Issue, error) {
	if m.createFn == nil {
		return nil, nil
	}
	return m.createFn(ctx, ownerUID, issue)
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

func (m *mockIssueRepo) UpdateState(ctx context.Context, projectID string, number int32, state model.IssueState) (*model.Issue, error) {
	if m.updateStateFn == nil {
		return nil, nil
	}
	return m.updateStateFn(ctx, projectID, number, state)
}

package application_test

import (
	"context"
	"time"

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
	createFn      func(ctx context.Context, workspaceIDs []string, issue *model.Issue, author model.Author) (*model.Issue, error)
	getByNumberFn func(ctx context.Context, projectID string, number int32) (*model.Issue, error)
	listFn        func(ctx context.Context, f port.IssueFilter) (*port.IssuePage, error)
	moveFn        func(ctx context.Context, projectID string, number int32, target *model.Status, place port.Placement, author model.Author) (*model.Issue, error)
	updateFn      func(ctx context.Context, projectID string, number int32, patch model.IssuePatch, author model.Author) (*model.Issue, error)
	addCommentFn  func(ctx context.Context, projectID string, number int32, c *model.Comment, author model.Author) (*model.Comment, error)
	listEventsFn  func(ctx context.Context, projectID string, number int32, page port.PageFilter) (*port.EventPage, error)
	discardFn     func(ctx context.Context, projectID string, number int32, statusID, reason string, author model.Author) (*model.Issue, error)
	restoreFn     func(ctx context.Context, projectID string, number int32, author model.Author) (*model.Issue, error)
	importFn      func(ctx context.Context, workspaceIDs []string, spec model.ImportSpec, author model.Author) (*model.Issue, model.ImportOutcome, error)
	importEvtsFn  func(ctx context.Context, workspaceIDs []string, projectID, sourceKey string, events []model.ImportedEvent, overwrite bool, author model.Author) (model.ImportEventsResult, error)
}

func (m *mockIssueRepo) ImportEvents(ctx context.Context, workspaceIDs []string, projectID, sourceKey string, events []model.ImportedEvent, overwrite bool, author model.Author) (model.ImportEventsResult, error) {
	if m.importEvtsFn == nil {
		return model.ImportEventsResult{}, nil
	}
	return m.importEvtsFn(ctx, workspaceIDs, projectID, sourceKey, events, overwrite, author)
}

func (m *mockIssueRepo) Import(ctx context.Context, workspaceIDs []string, spec model.ImportSpec, author model.Author) (*model.Issue, model.ImportOutcome, error) {
	if m.importFn == nil {
		return nil, "", nil
	}
	return m.importFn(ctx, workspaceIDs, spec, author)
}

var _ port.IssueRepository = (*mockIssueRepo)(nil)

func (m *mockIssueRepo) Create(ctx context.Context, workspaceIDs []string, issue *model.Issue, author model.Author) (*model.Issue, error) {
	if m.createFn == nil {
		return nil, nil
	}
	return m.createFn(ctx, workspaceIDs, issue, author)
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

func (m *mockIssueRepo) Move(ctx context.Context, projectID string, number int32, target *model.Status, place port.Placement, author model.Author) (*model.Issue, error) {
	if m.moveFn == nil {
		return nil, nil
	}
	return m.moveFn(ctx, projectID, number, target, place, author)
}

func (m *mockIssueRepo) Update(ctx context.Context, projectID string, number int32, patch model.IssuePatch, author model.Author) (*model.Issue, error) {
	if m.updateFn == nil {
		return nil, nil
	}
	return m.updateFn(ctx, projectID, number, patch, author)
}

func (m *mockIssueRepo) AddComment(ctx context.Context, projectID string, number int32, c *model.Comment, author model.Author) (*model.Comment, error) {
	if m.addCommentFn == nil {
		return nil, nil
	}
	return m.addCommentFn(ctx, projectID, number, c, author)
}

func (m *mockIssueRepo) Discard(ctx context.Context, projectID string, number int32, statusID, reason string, author model.Author) (*model.Issue, error) {
	if m.discardFn == nil {
		return nil, nil
	}
	return m.discardFn(ctx, projectID, number, statusID, reason, author)
}

func (m *mockIssueRepo) Restore(ctx context.Context, projectID string, number int32, author model.Author) (*model.Issue, error) {
	if m.restoreFn == nil {
		return nil, nil
	}
	return m.restoreFn(ctx, projectID, number, author)
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
	updateFn func(ctx context.Context, projectID, id string, patch port.StatusPatch) (*model.Status, error)
	deleteFn func(ctx context.Context, projectID, id, moveToID string, author model.Author) error
	batchFn  func(ctx context.Context, projectID string, specs []model.StatusSpec, deletions []model.StatusDeletion, author model.Author) ([]*model.Status, error)
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

func (m *mockStatusRepo) Update(ctx context.Context, projectID, id string, patch port.StatusPatch) (*model.Status, error) {
	if m.updateFn == nil {
		return nil, nil
	}
	return m.updateFn(ctx, projectID, id, patch)
}

func (m *mockStatusRepo) BatchUpdate(ctx context.Context, projectID string, specs []model.StatusSpec, deletions []model.StatusDeletion, author model.Author) ([]*model.Status, error) {
	if m.batchFn == nil {
		return nil, nil
	}
	return m.batchFn(ctx, projectID, specs, deletions, author)
}

func (m *mockStatusRepo) Delete(ctx context.Context, projectID, id, moveToID string, author model.Author) error {
	if m.deleteFn == nil {
		return nil
	}
	return m.deleteFn(ctx, projectID, id, moveToID, author)
}

// mockLabelRepo satisface port.LabelRepository.
type mockLabelRepo struct {
	listFn   func(ctx context.Context, projectID string) ([]*model.Label, error)
	createFn func(ctx context.Context, l *model.Label) (*model.Label, error)
	updateFn func(ctx context.Context, projectID, id string, patch port.LabelPatch) (*model.Label, error)
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

func (m *mockLabelRepo) Update(ctx context.Context, projectID, id string, patch port.LabelPatch) (*model.Label, error) {
	if m.updateFn == nil {
		return nil, nil
	}
	return m.updateFn(ctx, projectID, id, patch)
}

func (m *mockLabelRepo) Delete(ctx context.Context, projectID, id string) error {
	if m.deleteFn == nil {
		return nil
	}
	return m.deleteFn(ctx, projectID, id)
}

// as arma el autor de un cambio hecho por la persona uid (sin agente).
func as(uid string) model.Author { return model.Author{UserID: uid} }

// mockActorRepo satisface port.ActorRepository.
type mockActorRepo struct {
	ensurePersonFn func(ctx context.Context, person *model.Actor) (*model.Actor, error)
	getFn          func(ctx context.Context, id string) (*model.Actor, error)
	listAgentsFn   func(ctx context.Context, ownerID string, includeRevoked bool) ([]*model.Actor, error)
	createAgentFn  func(ctx context.Context, agent *model.Actor) (*model.Actor, error)
	revokeFn       func(ctx context.Context, id string, at time.Time) (*model.Actor, error)
}

var _ port.ActorRepository = (*mockActorRepo)(nil)

func (m *mockActorRepo) EnsurePerson(ctx context.Context, person *model.Actor) (*model.Actor, error) {
	if m.ensurePersonFn == nil {
		return person, nil
	}
	return m.ensurePersonFn(ctx, person)
}

func (m *mockActorRepo) Get(ctx context.Context, id string) (*model.Actor, error) {
	if m.getFn == nil {
		return nil, nil
	}
	return m.getFn(ctx, id)
}

func (m *mockActorRepo) ListAgents(ctx context.Context, ownerID string, includeRevoked bool) ([]*model.Actor, error) {
	if m.listAgentsFn == nil {
		return nil, nil
	}
	return m.listAgentsFn(ctx, ownerID, includeRevoked)
}

func (m *mockActorRepo) CreateAgent(ctx context.Context, agent *model.Actor) (*model.Actor, error) {
	if m.createAgentFn == nil {
		return agent, nil
	}
	return m.createAgentFn(ctx, agent)
}

func (m *mockActorRepo) Revoke(ctx context.Context, id string, at time.Time) (*model.Actor, error) {
	if m.revokeFn == nil {
		return nil, nil
	}
	return m.revokeFn(ctx, id, at)
}

// mockTokenRepo satisface port.TokenRepository.
type mockTokenRepo struct {
	createFn  func(ctx context.Context, token *model.Token, hash []byte) (*model.Token, error)
	getFn     func(ctx context.Context, id string) (*model.Token, error)
	listFn    func(ctx context.Context, actorIDs []string, includeInactive bool, now time.Time) ([]*model.Token, error)
	revokeFn  func(ctx context.Context, id string, at time.Time) (*model.Token, error)
	resolveFn func(ctx context.Context, hash []byte, now time.Time) (*model.ResolvedToken, error)
}

var _ port.TokenRepository = (*mockTokenRepo)(nil)

func (m *mockTokenRepo) Create(ctx context.Context, token *model.Token, hash []byte) (*model.Token, error) {
	if m.createFn == nil {
		return token, nil
	}
	return m.createFn(ctx, token, hash)
}

func (m *mockTokenRepo) Get(ctx context.Context, id string) (*model.Token, error) {
	if m.getFn == nil {
		return nil, nil
	}
	return m.getFn(ctx, id)
}

func (m *mockTokenRepo) List(ctx context.Context, actorIDs []string, includeInactive bool, now time.Time) ([]*model.Token, error) {
	if m.listFn == nil {
		return nil, nil
	}
	return m.listFn(ctx, actorIDs, includeInactive, now)
}

func (m *mockTokenRepo) Revoke(ctx context.Context, id string, at time.Time) (*model.Token, error) {
	if m.revokeFn == nil {
		return nil, nil
	}
	return m.revokeFn(ctx, id, at)
}

func (m *mockTokenRepo) Resolve(ctx context.Context, hash []byte, now time.Time) (*model.ResolvedToken, error) {
	if m.resolveFn == nil {
		return nil, nil
	}
	return m.resolveFn(ctx, hash, now)
}

// mockFlowRepo satisface port.FlowRepository.
type mockFlowRepo struct {
	listFn func(ctx context.Context, projectID string, labelIDs []string) ([]model.FlowIssue, error)
}

var _ port.FlowRepository = (*mockFlowRepo)(nil)

func (m *mockFlowRepo) ListFlowIssues(ctx context.Context, projectID string, labelIDs []string) ([]model.FlowIssue, error) {
	if m.listFn == nil {
		return nil, nil
	}
	return m.listFn(ctx, projectID, labelIDs)
}

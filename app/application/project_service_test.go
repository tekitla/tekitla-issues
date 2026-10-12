package application_test

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

const (
	owner      = "u1"
	validID    = "0b7e6a8e-6c1f-4c8e-9a51-2f3c4d5e6f70"
	otherOwner = "u2"
)

// ================================================================
// CreateProject
// ================================================================

func TestCreateProject_Success(t *testing.T) {
	var captured *model.Project
	var statuses []*model.Status
	repo := &mockProjectRepo{
		createFn: func(_ context.Context, p *model.Project, st []*model.Status) (*model.Project, error) {
			captured, statuses = p, st
			return p, nil
		},
	}

	got, err := application.NewProjectService(personalWorkspaces(), repo).CreateProject(context.Background(), owner, application.CreateProjectInput{
		Key:         " tk ",
		Name:        "  Tekitla ",
		Description: " tracker ",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("repo.Create was not called")
	}
	if _, err := uuid.Parse(got.ID); err != nil {
		t.Errorf("ID should be a generated UUID, got %q", got.ID)
	}
	if got.WorkspaceID != ownerWorkspace || got.Key != "TK" || got.Name != "Tekitla" || got.Description != "tracker" {
		t.Errorf("project: got %+v", got)
	}
	// Nace con un estado por categoría, en orden.
	var categories []model.IssueState
	for _, st := range statuses {
		if _, err := uuid.Parse(st.ID); err != nil || st.Name == "" {
			t.Errorf("status: got %+v", st)
		}
		categories = append(categories, st.Category)
	}
	want := []model.IssueState{model.IssueStateTodo, model.IssueStateInProgress, model.IssueStateDone, model.IssueStateCanceled}
	if !slices.Equal(categories, want) {
		t.Errorf("default statuses: got %v, want %v", categories, want)
	}
}

func TestCreateProject_Validation(t *testing.T) {
	repo := &mockProjectRepo{
		createFn: func(context.Context, *model.Project, []*model.Status) (*model.Project, error) {
			t.Fatal("repo.Create must not be called on invalid input")
			return nil, nil
		},
	}
	svc := application.NewProjectService(personalWorkspaces(), repo)

	for name, in := range map[string]application.CreateProjectInput{
		"bad key":    {Key: "1X", Name: "n"},
		"empty key":  {Key: "", Name: "n"},
		"empty name": {Key: "TK", Name: " "},
	} {
		if _, err := svc.CreateProject(context.Background(), owner, in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}
}

func TestCreateProject_RequiresOwner(t *testing.T) {
	_, err := application.NewProjectService(personalWorkspaces(), &mockProjectRepo{}).CreateProject(context.Background(), " ", application.CreateProjectInput{Key: "TK", Name: "n"})
	if !isUnauthenticated(err) {
		t.Fatalf("expected UnauthorizedError, got %v", err)
	}
}

func TestCreateProject_DuplicateKey(t *testing.T) {
	repo := &mockProjectRepo{
		createFn: func(context.Context, *model.Project, []*model.Status) (*model.Project, error) {
			return nil, domainerrors.ConflictError("project already exists", nil)
		},
	}

	_, err := application.NewProjectService(personalWorkspaces(), repo).CreateProject(context.Background(), owner, application.CreateProjectInput{Key: "TK", Name: "n"})
	if !domainerrors.IsConflict(err) {
		t.Fatalf("expected ConflictError, got %v", err)
	}
	var de *domainerrors.DomainError
	if !asDomainError(err, &de) || de.Reason != "PROJECT_KEY_TAKEN" {
		t.Errorf("reason: got %+v", de)
	}
}

func TestCreateProject_PropagatesInternal(t *testing.T) {
	repo := &mockProjectRepo{
		createFn: func(context.Context, *model.Project, []*model.Status) (*model.Project, error) {
			return nil, domainerrors.InternalError("failed to create project", nil)
		},
	}
	_, err := application.NewProjectService(personalWorkspaces(), repo).CreateProject(context.Background(), owner, application.CreateProjectInput{Key: "TK", Name: "n"})
	if !domainerrors.IsInternal(err) {
		t.Fatalf("expected InternalError, got %v", err)
	}
}

// ================================================================
// ListProjects
// ================================================================

func TestListProjects_FiltersByWorkspacesAndNormalizesLimit(t *testing.T) {
	cases := map[int32]int32{
		0:                            application.DefaultPageLimit,
		-3:                           application.DefaultPageLimit,
		10:                           10,
		application.MaxPageLimit + 1: application.MaxPageLimit,
	}
	for in, want := range cases {
		var gotWorkspaces []string
		var gotFilter port.PageFilter
		repo := &mockProjectRepo{
			listFn: func(_ context.Context, ws []string, f port.PageFilter) (*port.ProjectPage, error) {
				gotWorkspaces, gotFilter = ws, f
				return &port.ProjectPage{}, nil
			},
		}
		req := application.ListProjectsInput{Page: port.PageFilter{Cursor: "c", Limit: in}}
		if _, err := application.NewProjectService(personalWorkspaces(), repo).ListProjects(context.Background(), owner, req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !slices.Equal(gotWorkspaces, []string{ownerWorkspace}) || gotFilter.Limit != want || gotFilter.Cursor != "c" {
			t.Errorf("limit %d: got workspaces=%v filter=%+v, want limit %d", in, gotWorkspaces, gotFilter, want)
		}
	}
}

func TestListProjects_RequiresOwner(t *testing.T) {
	_, err := application.NewProjectService(personalWorkspaces(), &mockProjectRepo{}).ListProjects(context.Background(), "", application.ListProjectsInput{})
	if !isUnauthenticated(err) {
		t.Fatalf("expected UnauthorizedError, got %v", err)
	}
}

func TestListProjects_OwnWorkspaceFilter(t *testing.T) {
	var gotWorkspaces []string
	repo := &mockProjectRepo{
		listFn: func(_ context.Context, ws []string, _ port.PageFilter) (*port.ProjectPage, error) {
			gotWorkspaces = ws
			return &port.ProjectPage{}, nil
		},
	}
	in := application.ListProjectsInput{WorkspaceID: ownerWorkspace}
	if _, err := application.NewProjectService(personalWorkspaces(), repo).ListProjects(context.Background(), owner, in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(gotWorkspaces, []string{ownerWorkspace}) {
		t.Errorf("workspaces: got %v", gotWorkspaces)
	}
}

// Un espacio ajeno se pide al repo como lista vacía de espacios (página vacía,
// pero el repo igual valida el cursor): no revela que existe.
func TestListProjects_ForeignWorkspaceIsEmpty(t *testing.T) {
	var gotWorkspaces []string
	repo := &mockProjectRepo{
		listFn: func(_ context.Context, ws []string, _ port.PageFilter) (*port.ProjectPage, error) {
			gotWorkspaces = ws
			return &port.ProjectPage{}, nil
		},
	}
	in := application.ListProjectsInput{WorkspaceID: "ws-" + otherOwner}
	if _, err := application.NewProjectService(personalWorkspaces(), repo).ListProjects(context.Background(), owner, in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotWorkspaces == nil || len(gotWorkspaces) != 0 {
		t.Errorf("workspaces: got %#v, want empty", gotWorkspaces)
	}
}

func TestCreateProject_InOwnWorkspace(t *testing.T) {
	repo := &mockProjectRepo{
		createFn: func(_ context.Context, p *model.Project, _ []*model.Status) (*model.Project, error) { return p, nil },
	}
	in := application.CreateProjectInput{WorkspaceID: ownerWorkspace, Key: "TK", Name: "n"}
	got, err := application.NewProjectService(personalWorkspaces(), repo).CreateProject(context.Background(), owner, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.WorkspaceID != ownerWorkspace {
		t.Errorf("workspace: got %q", got.WorkspaceID)
	}
}

func TestCreateProject_ForeignWorkspaceIsNotFound(t *testing.T) {
	repo := &mockProjectRepo{
		createFn: func(context.Context, *model.Project, []*model.Status) (*model.Project, error) {
			t.Fatal("repo.Create must not be called for a foreign workspace")
			return nil, nil
		},
	}
	in := application.CreateProjectInput{WorkspaceID: "ws-" + otherOwner, Key: "TK", Name: "n"}
	_, err := application.NewProjectService(personalWorkspaces(), repo).CreateProject(context.Background(), owner, in)
	if !domainerrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

// Listar sin espacio no lo crea: la lista de espacios va vacía al repo.
func TestListProjects_UserWithoutWorkspace(t *testing.T) {
	var gotWorkspaces []string
	repo := &mockProjectRepo{
		listFn: func(_ context.Context, ws []string, _ port.PageFilter) (*port.ProjectPage, error) {
			gotWorkspaces = ws
			return &port.ProjectPage{}, nil
		},
	}
	if _, err := application.NewProjectService(noWorkspaces(t), repo).ListProjects(context.Background(), owner, application.ListProjectsInput{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotWorkspaces) != 0 {
		t.Errorf("workspaces: got %v, want empty", gotWorkspaces)
	}
}

func TestListProjects_PropagatesWorkspaceError(t *testing.T) {
	workspaces := &mockWorkspaceRepo{
		getPersonalFn: func(context.Context, string) (*model.Workspace, error) {
			return nil, domainerrors.InternalError("failed to get workspace", nil)
		},
	}
	_, err := application.NewProjectService(workspaces, &mockProjectRepo{}).ListProjects(context.Background(), owner, application.ListProjectsInput{})
	if !domainerrors.IsInternal(err) {
		t.Fatalf("expected InternalError, got %v", err)
	}
}

func TestCreateProject_PropagatesWorkspaceError(t *testing.T) {
	workspaces := &mockWorkspaceRepo{
		ensurePersonalFn: func(context.Context, *model.Workspace) (*model.Workspace, error) {
			return nil, domainerrors.InternalError("failed to create workspace", nil)
		},
	}
	repo := &mockProjectRepo{
		createFn: func(context.Context, *model.Project, []*model.Status) (*model.Project, error) {
			t.Fatal("repo.Create must not be called when the workspace fails")
			return nil, nil
		},
	}
	_, err := application.NewProjectService(workspaces, repo).CreateProject(context.Background(), owner, application.CreateProjectInput{Key: "TK", Name: "n"})
	if !domainerrors.IsInternal(err) {
		t.Fatalf("expected InternalError, got %v", err)
	}
}

// Crear en el espacio personal sin tenerlo lo crea (primer proyecto).
func TestCreateProject_CreatesPersonalWorkspace(t *testing.T) {
	var ensured string
	workspaces := &mockWorkspaceRepo{
		ensurePersonalFn: func(_ context.Context, ws *model.Workspace) (*model.Workspace, error) {
			ensured = ws.OwnerUID
			return ws, nil
		},
	}
	repo := &mockProjectRepo{
		createFn: func(_ context.Context, p *model.Project, _ []*model.Status) (*model.Project, error) { return p, nil },
	}
	got, err := application.NewProjectService(workspaces, repo).CreateProject(context.Background(), owner, application.CreateProjectInput{Key: "TK", Name: "n"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ensured != owner || got.WorkspaceID == "" {
		t.Errorf("ensured %q, project %+v", ensured, got)
	}
}

package application_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// personalWorkspaces devuelve un repo de espacios donde todo usuario ya tiene
// su espacio personal, "ws-<uid>".
func personalWorkspaces() *mockWorkspaceRepo {
	return &mockWorkspaceRepo{
		getPersonalFn: func(_ context.Context, ownerUID string) (*model.Workspace, error) {
			return &model.Workspace{ID: "ws-" + ownerUID, OwnerUID: ownerUID}, nil
		},
		ensurePersonalFn: func(_ context.Context, ws *model.Workspace) (*model.Workspace, error) {
			return &model.Workspace{ID: "ws-" + ws.OwnerUID, OwnerUID: ws.OwnerUID}, nil
		},
	}
}

// noWorkspaces devuelve un repo de espacios donde nadie tiene espacio todavía
// (ningún usuario creó un proyecto); EnsurePersonal falla el test.
func noWorkspaces(t *testing.T) *mockWorkspaceRepo {
	t.Helper()
	return &mockWorkspaceRepo{
		getPersonalFn: func(context.Context, string) (*model.Workspace, error) {
			return nil, domainerrors.NotFoundError("workspace not found", nil)
		},
		ensurePersonalFn: func(context.Context, *model.Workspace) (*model.Workspace, error) {
			t.Fatal("reads must not create the workspace")
			return nil, nil
		},
	}
}

// ownerWorkspace es el espacio personal de owner según personalWorkspaces.
const ownerWorkspace = "ws-" + owner

// ownedProjects devuelve un repo de proyectos donde validID es del espacio de
// owner y cualquier otra combinación da NotFound (igual que el repo real).
func ownedProjects() *mockProjectRepo {
	return &mockProjectRepo{
		getByIDFn: func(_ context.Context, workspaceIDs []string, id string) (*model.Project, error) {
			if slices.Contains(workspaceIDs, ownerWorkspace) && id == validID {
				return &model.Project{ID: id, WorkspaceID: ownerWorkspace, Key: "TK"}, nil
			}
			return nil, domainerrors.NotFoundError("project not found", map[string]any{"id": id})
		},
	}
}

// failIssueRepo falla el test si el service llega al repo de issues.
func failIssueRepo(t *testing.T) *mockIssueRepo {
	t.Helper()
	fail := func() { t.Helper(); t.Fatal("issue repo must not be called") }
	return &mockIssueRepo{
		createFn: func(context.Context, []string, *model.Issue, string) (*model.Issue, error) {
			fail()
			return nil, nil
		},
		getByNumberFn: func(context.Context, string, int32) (*model.Issue, error) {
			fail()
			return nil, nil
		},
		listFn: func(context.Context, port.IssueFilter) (*port.IssuePage, error) { fail(); return nil, nil },
		moveFn: func(context.Context, string, int32, *model.Status, port.Placement, string) (*model.Issue, error) {
			fail()
			return nil, nil
		},
		updateFn: func(context.Context, string, int32, model.IssuePatch, string) (*model.Issue, error) {
			fail()
			return nil, nil
		},
		addCommentFn: func(context.Context, string, int32, *model.Comment) (*model.Comment, error) {
			fail()
			return nil, nil
		},
		listEventsFn: func(context.Context, string, int32, port.PageFilter) (*port.EventPage, error) {
			fail()
			return nil, nil
		},
	}
}

// Estados del proyecto validID en los tests: los cuatro de un proyecto nuevo.
var (
	stTodo     = &model.Status{ID: "11111111-1111-4111-8111-111111111111", ProjectID: validID, Name: "Por hacer", Category: model.IssueStateTodo, Rank: "a0"}
	stDoing    = &model.Status{ID: "22222222-2222-4222-8222-222222222222", ProjectID: validID, Name: "En curso", Category: model.IssueStateInProgress, Rank: "a1"}
	stDone     = &model.Status{ID: "33333333-3333-4333-8333-333333333333", ProjectID: validID, Name: "Hecho", Category: model.IssueStateDone, Rank: "a2"}
	stCanceled = &model.Status{ID: "44444444-4444-4444-8444-444444444444", ProjectID: validID, Name: "Descartado", Category: model.IssueStateCanceled, Rank: "a3"}
	// stReview es un segundo estado «en curso», que no existe por default.
	stReview = &model.Status{ID: "55555555-5555-4555-8555-555555555555", ProjectID: validID, Name: "En revisión", Category: model.IssueStateInProgress, Rank: "a1V"}
)

// projectStatuses devuelve un repo de estados con los cuatro de default (más
// los extra, en orden de rank). Get de un id que no está da NotFound.
func projectStatuses(extra ...*model.Status) *mockStatusRepo {
	all := []*model.Status{stTodo, stDoing}
	all = append(all, extra...)
	all = append(all, stDone, stCanceled)
	return &mockStatusRepo{
		listFn: func(context.Context, string) ([]*model.Status, error) { return all, nil },
		getFn: func(_ context.Context, _, id string) (*model.Status, error) {
			for _, st := range all {
				if st.ID == id {
					return st, nil
				}
			}
			return nil, domainerrors.NotFoundError("status not found", nil)
		},
	}
}

// newIssueSvc arma el service con los estados de default y sin etiquetas.
func newIssueSvc(ws port.WorkspaceRepository, projects port.ProjectRepository, issues port.IssueRepository) *application.IssueService {
	return application.NewIssueService(ws, projects, issues, projectStatuses(), &mockLabelRepo{})
}

func isUnauthenticated(err error) bool {
	var de *domainerrors.DomainError
	return asDomainError(err, &de) && de.Code == codes.Unauthenticated
}

func asDomainError(err error, target **domainerrors.DomainError) bool {
	return errors.As(err, target)
}

// ================================================================
// Pertenencia: proyecto ajeno / inexistente / sin usuario
// ================================================================

func TestIssueService_ForeignProjectIsNotFound(t *testing.T) {
	svc := newIssueSvc(personalWorkspaces(), ownedProjects(), failIssueRepo(t))
	ctx := context.Background()

	if _, err := svc.ListIssues(ctx, otherOwner, application.ListIssuesInput{ProjectID: validID}); !domainerrors.IsNotFound(err) {
		t.Errorf("ListIssues: expected NotFound, got %v", err)
	}
	if _, err := svc.GetIssue(ctx, otherOwner, validID, 1); !domainerrors.IsNotFound(err) {
		t.Errorf("GetIssue: expected NotFound, got %v", err)
	}
	if _, err := svc.CreateIssue(ctx, otherOwner, application.CreateIssueInput{ProjectID: validID, Title: "x"}); !domainerrors.IsNotFound(err) {
		t.Errorf("CreateIssue: expected NotFound, got %v", err)
	}
	if _, err := svc.MoveIssue(ctx, otherOwner, application.MoveIssueInput{ProjectID: validID, Number: 1, State: model.IssueStateDone}); !domainerrors.IsNotFound(err) {
		t.Errorf("MoveIssue: expected NotFound, got %v", err)
	}
}

func TestIssueService_RequiresOwner(t *testing.T) {
	svc := newIssueSvc(personalWorkspaces(), ownedProjects(), failIssueRepo(t))
	if _, err := svc.GetIssue(context.Background(), "", validID, 1); !isUnauthenticated(err) {
		t.Errorf("expected UnauthorizedError, got %v", err)
	}
}

func TestIssueService_InvalidProjectID(t *testing.T) {
	svc := newIssueSvc(personalWorkspaces(), ownedProjects(), failIssueRepo(t))
	for _, id := range []string{"", "not-a-uuid"} {
		if _, err := svc.ListIssues(context.Background(), owner, application.ListIssuesInput{ProjectID: id}); !domainerrors.IsValidation(err) {
			t.Errorf("project_id %q: expected ValidationError, got %v", id, err)
		}
	}
}

// ================================================================
// CreateIssue
// ================================================================

func TestCreateIssue_DefaultsPriorityAndState(t *testing.T) {
	var gotWorkspaces []string
	issues := &mockIssueRepo{
		createFn: func(_ context.Context, ws []string, issue *model.Issue, actor string) (*model.Issue, error) {
			gotWorkspaces = ws
			if actor != owner {
				t.Errorf("actor: got %q", actor)
			}
			issue.Number = 1
			return issue, nil
		},
	}

	got, err := newIssueSvc(personalWorkspaces(), ownedProjects(), issues).CreateIssue(context.Background(), owner, application.CreateIssueInput{
		ProjectID:   validID,
		Title:       "  Primer issue ",
		Description: " cuerpo ",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(gotWorkspaces, []string{ownerWorkspace}) {
		t.Errorf("workspaces passed to repo: got %v", gotWorkspaces)
	}
	if got.Priority != model.PriorityMedium {
		t.Errorf("priority: got %d, want Medium", got.Priority)
	}
	if got.State != model.IssueStateTodo || got.StatusID != stTodo.ID {
		t.Errorf("state: got %q / %q, want the first todo status", got.State, got.StatusID)
	}
	if got.Title != "Primer issue" || got.Description != "cuerpo" || got.ProjectID != validID || got.ID == "" {
		t.Errorf("issue: got %+v", got)
	}
}

func TestCreateIssue_KeepsExplicitPriority(t *testing.T) {
	issues := &mockIssueRepo{
		createFn: func(_ context.Context, _ []string, issue *model.Issue, _ string) (*model.Issue, error) {
			return issue, nil
		},
	}
	got, err := newIssueSvc(personalWorkspaces(), ownedProjects(), issues).CreateIssue(context.Background(), owner, application.CreateIssueInput{
		ProjectID: validID, Title: "x", Priority: model.PriorityUrgent,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Priority != model.PriorityUrgent {
		t.Errorf("priority: got %d, want Urgent", got.Priority)
	}
}

func TestCreateIssue_Validation(t *testing.T) {
	svc := newIssueSvc(personalWorkspaces(), ownedProjects(), failIssueRepo(t))
	for name, in := range map[string]application.CreateIssueInput{
		"empty title":      {ProjectID: validID, Title: "  "},
		"unknown priority": {ProjectID: validID, Title: "x", Priority: model.Priority(9)},
	} {
		if _, err := svc.CreateIssue(context.Background(), owner, in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}
}

func TestCreateIssue_PropagatesRepoError(t *testing.T) {
	issues := &mockIssueRepo{
		createFn: func(context.Context, []string, *model.Issue, string) (*model.Issue, error) {
			return nil, domainerrors.InternalError("failed to create issue", nil)
		},
	}
	_, err := newIssueSvc(personalWorkspaces(), ownedProjects(), issues).CreateIssue(context.Background(), owner, application.CreateIssueInput{ProjectID: validID, Title: "x"})
	if !domainerrors.IsInternal(err) {
		t.Fatalf("expected InternalError, got %v", err)
	}
}

// ================================================================
// MoveIssue
// ================================================================

// todoIssue: el issue number está en «Por hacer».
func todoIssue(_ context.Context, projectID string, number int32) (*model.Issue, error) {
	return &model.Issue{ProjectID: projectID, Number: number, State: model.IssueStateTodo, StatusID: stTodo.ID}, nil
}

// moveTo devuelve el issue en el estado destino.
func moveTo(_ context.Context, projectID string, number int32, target *model.Status, _ port.Placement, _ string) (*model.Issue, error) {
	return &model.Issue{ProjectID: projectID, Number: number, State: target.Category, StatusID: target.ID}, nil
}

func TestMoveIssue_Success(t *testing.T) {
	issues := &mockIssueRepo{getByNumberFn: todoIssue, moveFn: moveTo}
	got, err := newIssueSvc(personalWorkspaces(), ownedProjects(), issues).MoveIssue(context.Background(), owner, application.MoveIssueInput{
		ProjectID: validID, Number: 3, State: model.IssueStateInProgress,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Number != 3 || got.State != model.IssueStateInProgress || got.StatusID != stDoing.ID {
		t.Errorf("issue: got %+v", got)
	}
}

func TestMoveIssue_InvalidState(t *testing.T) {
	svc := newIssueSvc(personalWorkspaces(), ownedProjects(), failIssueRepo(t))
	for _, st := range []model.IssueState{model.IssueStateUnspecified, "blocked"} {
		_, err := svc.MoveIssue(context.Background(), owner, application.MoveIssueInput{ProjectID: validID, Number: 1, State: st})
		if !domainerrors.IsValidation(err) {
			t.Errorf("state %q: expected ValidationError, got %v", st, err)
		}
	}
}

func TestMoveIssue_InvalidNumber(t *testing.T) {
	svc := newIssueSvc(personalWorkspaces(), ownedProjects(), failIssueRepo(t))
	_, err := svc.MoveIssue(context.Background(), owner, application.MoveIssueInput{ProjectID: validID, Number: 0, State: model.IssueStateDone})
	if !domainerrors.IsValidation(err) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
}

func TestMoveIssue_PropagatesNotFound(t *testing.T) {
	issues := &mockIssueRepo{
		getByNumberFn: func(context.Context, string, int32) (*model.Issue, error) {
			return nil, domainerrors.NotFoundError("issue not found", nil)
		},
	}
	_, err := newIssueSvc(personalWorkspaces(), ownedProjects(), issues).MoveIssue(context.Background(), owner, application.MoveIssueInput{
		ProjectID: validID, Number: 99, State: model.IssueStateDone,
	})
	if !domainerrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

// ================================================================
// GetIssue / ListIssues
// ================================================================

func TestGetIssue_Success(t *testing.T) {
	issues := &mockIssueRepo{
		getByNumberFn: func(_ context.Context, projectID string, number int32) (*model.Issue, error) {
			return &model.Issue{ProjectID: projectID, Number: number}, nil
		},
	}
	got, err := newIssueSvc(personalWorkspaces(), ownedProjects(), issues).GetIssue(context.Background(), owner, validID, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Number != 7 {
		t.Errorf("number: got %d", got.Number)
	}
}

func TestGetIssue_InvalidNumber(t *testing.T) {
	svc := newIssueSvc(personalWorkspaces(), ownedProjects(), failIssueRepo(t))
	if _, err := svc.GetIssue(context.Background(), owner, validID, -1); !domainerrors.IsValidation(err) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
}

func TestListIssues_PassesFilter(t *testing.T) {
	var got port.IssueFilter
	issues := &mockIssueRepo{
		listFn: func(_ context.Context, f port.IssueFilter) (*port.IssuePage, error) {
			got = f
			return &port.IssuePage{NextCursor: "next", HasNext: true}, nil
		},
	}
	page, err := newIssueSvc(personalWorkspaces(), ownedProjects(), issues).ListIssues(context.Background(), owner, application.ListIssuesInput{
		ProjectID: validID, State: model.IssueStateDone, Cursor: "abc", Limit: 500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := port.IssueFilter{ProjectID: validID, States: []model.IssueState{model.IssueStateDone}, Order: model.IssueOrderRank, Cursor: "abc", Limit: application.MaxPageLimit}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filter: got %+v, want %+v", got, want)
	}
	if !page.HasNext || page.NextCursor != "next" {
		t.Errorf("page: got %+v", page)
	}
}

func TestListIssues_InvalidStateFilter(t *testing.T) {
	svc := newIssueSvc(personalWorkspaces(), ownedProjects(), failIssueRepo(t))
	_, err := svc.ListIssues(context.Background(), owner, application.ListIssuesInput{ProjectID: validID, State: "blocked"})
	if !domainerrors.IsValidation(err) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
}

func TestMoveIssue_Canceled(t *testing.T) {
	issues := &mockIssueRepo{getByNumberFn: todoIssue, moveFn: moveTo}
	got, err := newIssueSvc(personalWorkspaces(), ownedProjects(), issues).MoveIssue(context.Background(), owner, application.MoveIssueInput{
		ProjectID: validID, Number: 3, State: model.IssueStateCanceled,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.State != model.IssueStateCanceled {
		t.Errorf("state: got %q, want canceled", got.State)
	}
}

func TestIssueService_PropagatesWorkspaceError(t *testing.T) {
	workspaces := &mockWorkspaceRepo{
		getPersonalFn: func(context.Context, string) (*model.Workspace, error) {
			return nil, domainerrors.InternalError("failed to get workspace", nil)
		},
	}
	svc := newIssueSvc(workspaces, ownedProjects(), failIssueRepo(t))
	if _, err := svc.GetIssue(context.Background(), owner, validID, 1); !domainerrors.IsInternal(err) {
		t.Fatalf("expected InternalError, got %v", err)
	}
}

// Un usuario sin espacio (nunca creó un proyecto) no ve proyectos ajenos y la
// lectura no crea el espacio.
func TestIssueService_UserWithoutWorkspaceIsNotFound(t *testing.T) {
	svc := newIssueSvc(noWorkspaces(t), ownedProjects(), failIssueRepo(t))
	if _, err := svc.GetIssue(context.Background(), owner, validID, 1); !domainerrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

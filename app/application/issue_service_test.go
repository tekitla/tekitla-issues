package application_test

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/vrojas1015/tequio-issues/app/application"
	domainerrors "github.com/vrojas1015/tequio-issues/app/domain/errors"
	"github.com/vrojas1015/tequio-issues/app/domain/model"
	"github.com/vrojas1015/tequio-issues/app/domain/port"
)

// ownedProjects devuelve un repo de proyectos donde validID es de owner y
// cualquier otra combinación da NotFound (igual que el repo real).
func ownedProjects() *mockProjectRepo {
	return &mockProjectRepo{
		getByIDFn: func(_ context.Context, ownerUID, id string) (*model.Project, error) {
			if ownerUID == owner && id == validID {
				return &model.Project{ID: id, OwnerUID: ownerUID, Key: "TQ"}, nil
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
		createFn: func(context.Context, string, *model.Issue) (*model.Issue, error) { fail(); return nil, nil },
		getByNumberFn: func(context.Context, string, int32) (*model.Issue, error) {
			fail()
			return nil, nil
		},
		listFn: func(context.Context, port.IssueFilter) (*port.IssuePage, error) { fail(); return nil, nil },
		updateStateFn: func(context.Context, string, int32, model.IssueState) (*model.Issue, error) {
			fail()
			return nil, nil
		},
	}
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
	svc := application.NewIssueService(ownedProjects(), failIssueRepo(t))
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
	svc := application.NewIssueService(ownedProjects(), failIssueRepo(t))
	if _, err := svc.GetIssue(context.Background(), "", validID, 1); !isUnauthenticated(err) {
		t.Errorf("expected UnauthorizedError, got %v", err)
	}
}

func TestIssueService_InvalidProjectID(t *testing.T) {
	svc := application.NewIssueService(ownedProjects(), failIssueRepo(t))
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
	var gotOwner string
	issues := &mockIssueRepo{
		createFn: func(_ context.Context, o string, issue *model.Issue) (*model.Issue, error) {
			gotOwner = o
			issue.Number = 1
			return issue, nil
		},
	}

	got, err := application.NewIssueService(ownedProjects(), issues).CreateIssue(context.Background(), owner, application.CreateIssueInput{
		ProjectID:   validID,
		Title:       "  Primer issue ",
		Description: " cuerpo ",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOwner != owner {
		t.Errorf("owner passed to repo: got %q", gotOwner)
	}
	if got.Priority != model.PriorityMedium {
		t.Errorf("priority: got %d, want Medium", got.Priority)
	}
	if got.State != model.IssueStateTodo {
		t.Errorf("state: got %q, want todo", got.State)
	}
	if got.Title != "Primer issue" || got.Description != "cuerpo" || got.ProjectID != validID || got.ID == "" {
		t.Errorf("issue: got %+v", got)
	}
}

func TestCreateIssue_KeepsExplicitPriority(t *testing.T) {
	issues := &mockIssueRepo{
		createFn: func(_ context.Context, _ string, issue *model.Issue) (*model.Issue, error) { return issue, nil },
	}
	got, err := application.NewIssueService(ownedProjects(), issues).CreateIssue(context.Background(), owner, application.CreateIssueInput{
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
	svc := application.NewIssueService(ownedProjects(), failIssueRepo(t))
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
		createFn: func(context.Context, string, *model.Issue) (*model.Issue, error) {
			return nil, domainerrors.InternalError("failed to create issue", nil)
		},
	}
	_, err := application.NewIssueService(ownedProjects(), issues).CreateIssue(context.Background(), owner, application.CreateIssueInput{ProjectID: validID, Title: "x"})
	if !domainerrors.IsInternal(err) {
		t.Fatalf("expected InternalError, got %v", err)
	}
}

// ================================================================
// MoveIssue
// ================================================================

func TestMoveIssue_Success(t *testing.T) {
	issues := &mockIssueRepo{
		updateStateFn: func(_ context.Context, projectID string, number int32, state model.IssueState) (*model.Issue, error) {
			return &model.Issue{ProjectID: projectID, Number: number, State: state}, nil
		},
	}
	got, err := application.NewIssueService(ownedProjects(), issues).MoveIssue(context.Background(), owner, application.MoveIssueInput{
		ProjectID: validID, Number: 3, State: model.IssueStateInProgress,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Number != 3 || got.State != model.IssueStateInProgress {
		t.Errorf("issue: got %+v", got)
	}
}

func TestMoveIssue_InvalidState(t *testing.T) {
	svc := application.NewIssueService(ownedProjects(), failIssueRepo(t))
	for _, st := range []model.IssueState{model.IssueStateUnspecified, "blocked"} {
		_, err := svc.MoveIssue(context.Background(), owner, application.MoveIssueInput{ProjectID: validID, Number: 1, State: st})
		if !domainerrors.IsValidation(err) {
			t.Errorf("state %q: expected ValidationError, got %v", st, err)
		}
	}
}

func TestMoveIssue_InvalidNumber(t *testing.T) {
	svc := application.NewIssueService(ownedProjects(), failIssueRepo(t))
	_, err := svc.MoveIssue(context.Background(), owner, application.MoveIssueInput{ProjectID: validID, Number: 0, State: model.IssueStateDone})
	if !domainerrors.IsValidation(err) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
}

func TestMoveIssue_PropagatesNotFound(t *testing.T) {
	issues := &mockIssueRepo{
		updateStateFn: func(context.Context, string, int32, model.IssueState) (*model.Issue, error) {
			return nil, domainerrors.NotFoundError("issue not found", nil)
		},
	}
	_, err := application.NewIssueService(ownedProjects(), issues).MoveIssue(context.Background(), owner, application.MoveIssueInput{
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
	got, err := application.NewIssueService(ownedProjects(), issues).GetIssue(context.Background(), owner, validID, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Number != 7 {
		t.Errorf("number: got %d", got.Number)
	}
}

func TestGetIssue_InvalidNumber(t *testing.T) {
	svc := application.NewIssueService(ownedProjects(), failIssueRepo(t))
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
	page, err := application.NewIssueService(ownedProjects(), issues).ListIssues(context.Background(), owner, application.ListIssuesInput{
		ProjectID: validID, State: model.IssueStateDone, Cursor: "abc", Limit: 500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := port.IssueFilter{ProjectID: validID, State: model.IssueStateDone, Cursor: "abc", Limit: application.MaxPageLimit}
	if got != want {
		t.Errorf("filter: got %+v, want %+v", got, want)
	}
	if !page.HasNext || page.NextCursor != "next" {
		t.Errorf("page: got %+v", page)
	}
}

func TestListIssues_InvalidStateFilter(t *testing.T) {
	svc := application.NewIssueService(ownedProjects(), failIssueRepo(t))
	_, err := svc.ListIssues(context.Background(), owner, application.ListIssuesInput{ProjectID: validID, State: "blocked"})
	if !domainerrors.IsValidation(err) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
}

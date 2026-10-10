package application_test

import (
	"context"
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
	repo := &mockProjectRepo{
		createFn: func(_ context.Context, p *model.Project) (*model.Project, error) {
			captured = p
			return p, nil
		},
	}

	got, err := application.NewProjectService(repo).CreateProject(context.Background(), owner, application.CreateProjectInput{
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
	if got.OwnerUID != owner || got.Key != "TK" || got.Name != "Tekitla" || got.Description != "tracker" {
		t.Errorf("project: got %+v", got)
	}
}

func TestCreateProject_Validation(t *testing.T) {
	repo := &mockProjectRepo{
		createFn: func(context.Context, *model.Project) (*model.Project, error) {
			t.Fatal("repo.Create must not be called on invalid input")
			return nil, nil
		},
	}
	svc := application.NewProjectService(repo)

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
	_, err := application.NewProjectService(&mockProjectRepo{}).CreateProject(context.Background(), " ", application.CreateProjectInput{Key: "TK", Name: "n"})
	if !isUnauthenticated(err) {
		t.Fatalf("expected UnauthorizedError, got %v", err)
	}
}

func TestCreateProject_DuplicateKey(t *testing.T) {
	repo := &mockProjectRepo{
		createFn: func(context.Context, *model.Project) (*model.Project, error) {
			return nil, domainerrors.ConflictError("project already exists", nil)
		},
	}

	_, err := application.NewProjectService(repo).CreateProject(context.Background(), owner, application.CreateProjectInput{Key: "TK", Name: "n"})
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
		createFn: func(context.Context, *model.Project) (*model.Project, error) {
			return nil, domainerrors.InternalError("failed to create project", nil)
		},
	}
	_, err := application.NewProjectService(repo).CreateProject(context.Background(), owner, application.CreateProjectInput{Key: "TK", Name: "n"})
	if !domainerrors.IsInternal(err) {
		t.Fatalf("expected InternalError, got %v", err)
	}
}

// ================================================================
// ListProjects
// ================================================================

func TestListProjects_FiltersByOwnerAndNormalizesLimit(t *testing.T) {
	cases := map[int32]int32{
		0:                            application.DefaultPageLimit,
		-3:                           application.DefaultPageLimit,
		10:                           10,
		application.MaxPageLimit + 1: application.MaxPageLimit,
	}
	for in, want := range cases {
		var gotOwner string
		var gotFilter port.PageFilter
		repo := &mockProjectRepo{
			listFn: func(_ context.Context, o string, f port.PageFilter) (*port.ProjectPage, error) {
				gotOwner, gotFilter = o, f
				return &port.ProjectPage{}, nil
			},
		}
		if _, err := application.NewProjectService(repo).ListProjects(context.Background(), owner, port.PageFilter{Cursor: "c", Limit: in}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotOwner != owner || gotFilter.Limit != want || gotFilter.Cursor != "c" {
			t.Errorf("limit %d: got owner=%q filter=%+v, want limit %d", in, gotOwner, gotFilter, want)
		}
	}
}

func TestListProjects_RequiresOwner(t *testing.T) {
	_, err := application.NewProjectService(&mockProjectRepo{}).ListProjects(context.Background(), "", port.PageFilter{})
	if !isUnauthenticated(err) {
		t.Fatalf("expected UnauthorizedError, got %v", err)
	}
}

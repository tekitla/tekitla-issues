package application_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// Casos de uso de los issues 011 a 013 (tekitla-protos v0.4.0).

const otherID = "99999999-9999-4999-8999-999999999999"

// --- 011: DiscardIssue / RestoreIssue ---

func TestDiscardIssue_DefaultsToFirstCanceled(t *testing.T) {
	var gotTarget *model.Status
	var gotReason, gotActor string
	issues := &mockIssueRepo{discardFn: func(_ context.Context, _ string, n int32, target *model.Status, reason, actor string) (*model.Issue, error) {
		gotTarget, gotReason, gotActor = target, reason, actor
		return &model.Issue{Number: n}, nil
	}}
	_, err := newIssueSvc(personalWorkspaces(), ownedProjects(), issues).DiscardIssue(context.Background(), owner, application.DiscardIssueInput{
		ProjectID: validID, Number: 3, Reason: "  duplicado  ",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTarget != stCanceled || gotReason != "duplicado" || gotActor != owner {
		t.Errorf("discard: target %v reason %q actor %q", gotTarget, gotReason, gotActor)
	}
}

func TestDiscardIssue_Validation(t *testing.T) {
	svc := newIssueSvc(personalWorkspaces(), ownedProjects(), failIssueRepo(t))
	cases := map[string]application.DiscardIssueInput{
		"sin motivo":            {Number: 1, Reason: "   "},
		"número inválido":       {Number: 0, Reason: "x"},
		"status_id no UUID":     {Number: 1, Reason: "x", StatusID: "abc"},
		"status_id ajeno":       {Number: 1, Reason: "x", StatusID: otherID},
		"status_id no CANCELED": {Number: 1, Reason: "x", StatusID: stDone.ID},
	}
	for name, in := range cases {
		in.ProjectID = validID
		if _, err := svc.DiscardIssue(context.Background(), owner, in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}
}

func TestDiscardIssue_PropagatesAlreadyDiscarded(t *testing.T) {
	issues := &mockIssueRepo{discardFn: func(context.Context, string, int32, *model.Status, string, string) (*model.Issue, error) {
		return nil, domainerrors.FailedPreconditionError("issue is already discarded", nil).WithReason("ISSUE_ALREADY_DISCARDED")
	}}
	_, err := newIssueSvc(personalWorkspaces(), ownedProjects(), issues).DiscardIssue(context.Background(), owner, application.DiscardIssueInput{
		ProjectID: validID, Number: 1, Reason: "x", StatusID: stCanceled.ID,
	})
	if !domainerrors.IsFailedPrecondition(err) {
		t.Errorf("expected FailedPrecondition, got %v", err)
	}
}

func TestRestoreIssue_GoesToFirstTodo(t *testing.T) {
	var gotTarget *model.Status
	issues := &mockIssueRepo{restoreFn: func(_ context.Context, _ string, n int32, target *model.Status, _ string) (*model.Issue, error) {
		gotTarget = target
		return &model.Issue{Number: n}, nil
	}}
	svc := newIssueSvc(personalWorkspaces(), ownedProjects(), issues)
	if _, err := svc.RestoreIssue(context.Background(), owner, validID, 2); err != nil || gotTarget != stTodo {
		t.Errorf("restore: target %v, err %v", gotTarget, err)
	}
	if _, err := svc.RestoreIssue(context.Background(), owner, validID, -1); !domainerrors.IsValidation(err) {
		t.Errorf("número inválido: expected ValidationError, got %v", err)
	}
	if _, err := svc.RestoreIssue(context.Background(), "u-ajeno", validID, 2); !domainerrors.IsNotFound(err) {
		t.Errorf("proyecto ajeno: expected NotFound, got %v", err)
	}
}

// --- 012: ListIssues ---

func TestListIssues_Filters(t *testing.T) {
	var got port.IssueFilter
	issues := &mockIssueRepo{listFn: func(_ context.Context, f port.IssueFilter) (*port.IssuePage, error) {
		got = f
		return &port.IssuePage{}, nil
	}}
	labels := &mockLabelRepo{listFn: func(context.Context, string) ([]*model.Label, error) {
		return []*model.Label{{ID: "l1"}, {ID: "l2"}}, nil
	}}
	svc := application.NewIssueService(personalWorkspaces(), ownedProjects(), issues, projectStatuses(), labels)
	_, err := svc.ListIssues(context.Background(), owner, application.ListIssuesInput{
		ProjectID:  validID,
		States:     []model.IssueState{model.IssueStateTodo, model.IssueStateInProgress, model.IssueStateTodo},
		StatusIDs:  []string{stDone.ID},
		Priorities: []model.Priority{model.PriorityHigh},
		LabelIDs:   []string{"l2", "l2"},
		Query:      " tk-4 ",
		Order:      model.IssueOrderPriorityDesc,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(got.States, []model.IssueState{model.IssueStateTodo, model.IssueStateInProgress}) ||
		!slices.Equal(got.StatusIDs, []string{stDone.ID}) || !slices.Equal(got.LabelIDs, []string{"l2"}) ||
		got.Search != (model.IssueSearch{Text: "tk-4", Number: 4}) || got.Order != model.IssueOrderPriorityDesc ||
		got.Limit != application.DefaultPageLimit {
		t.Errorf("filter: got %+v", got)
	}
}

func TestListIssues_FilterValidation(t *testing.T) {
	svc := newIssueSvc(personalWorkspaces(), ownedProjects(), failIssueRepo(t))
	cases := map[string]application.ListIssuesInput{
		"state + states":         {State: model.IssueStateTodo, States: []model.IssueState{model.IssueStateDone}},
		"states con unspecified": {States: []model.IssueState{model.IssueStateUnspecified}},
		"prioridad unspecified":  {Priorities: []model.Priority{model.PriorityUnspecified}},
		"status de otro":         {StatusIDs: []string{otherID}},
		"etiqueta de otro":       {LabelIDs: []string{"l9"}},
		"orden desconocido":      {Order: "random"},
	}
	for name, in := range cases {
		in.ProjectID = validID
		if _, err := svc.ListIssues(context.Background(), owner, in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}
}

// --- 013: estados ---

func TestCreateStatus_Defaults(t *testing.T) {
	var got *model.Status
	repo := &mockStatusRepo{createFn: func(_ context.Context, s *model.Status, _ string) (*model.Status, error) {
		got = s
		return s, nil
	}}
	svc := application.NewStatusService(personalWorkspaces(), ownedProjects(), repo)
	if _, err := svc.CreateStatus(context.Background(), owner, application.CreateStatusInput{ProjectID: validID, Name: "Archivo", Category: model.IssueStateCanceled}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Color != model.LabelColorDanger || !got.Hidden || got.WIPLimit != 0 {
		t.Errorf("CANCELED: got %+v", got)
	}
	visible := false
	if _, err := svc.CreateStatus(context.Background(), owner, application.CreateStatusInput{
		ProjectID: validID, Name: "Archivo 2", Category: model.IssueStateCanceled, Hidden: &visible, Color: model.LabelColorAccent, WIPLimit: 4,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Color != model.LabelColorAccent || got.Hidden || got.WIPLimit != 4 {
		t.Errorf("explícito: got %+v", got)
	}
	for name, in := range map[string]application.CreateStatusInput{
		"wip negativo":   {Name: "X", Category: model.IssueStateTodo, WIPLimit: -2},
		"color inválido": {Name: "X", Category: model.IssueStateTodo, Color: 11},
	} {
		in.ProjectID = validID
		if _, err := svc.CreateStatus(context.Background(), owner, in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}
}

func TestUpdateStatus_Settings(t *testing.T) {
	var got port.StatusPatch
	repo := &mockStatusRepo{updateFn: func(_ context.Context, _, id string, patch port.StatusPatch) (*model.Status, error) {
		got = patch
		return &model.Status{ID: id}, nil
	}}
	svc := application.NewStatusService(personalWorkspaces(), ownedProjects(), repo)
	color, wip, hidden := model.LabelColorUnspecified, int32(5), true
	if _, err := svc.UpdateStatus(context.Background(), owner, application.UpdateStatusInput{
		ProjectID: validID, StatusID: stDoing.ID, Color: &color, WIPLimit: &wip, Hidden: &hidden,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Color == nil || *got.Color != model.LabelColorUnspecified || *got.WIPLimit != 5 || !*got.Hidden || got.Name != nil {
		t.Errorf("patch: got %+v", got)
	}
	bad, neg := model.LabelColor(12), int32(-1)
	for name, in := range map[string]application.UpdateStatusInput{
		"color inválido": {Color: &bad},
		"wip negativo":   {WIPLimit: &neg},
	} {
		in.ProjectID, in.StatusID = validID, stDoing.ID
		if _, err := svc.UpdateStatus(context.Background(), owner, in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}
}

func TestBatchUpdateStatuses(t *testing.T) {
	var gotSpecs []model.StatusSpec
	var gotActor string
	repo := &mockStatusRepo{batchFn: func(_ context.Context, _ string, specs []model.StatusSpec, _ []model.StatusDeletion, actor string) ([]*model.Status, error) {
		gotSpecs, gotActor = specs, actor
		return []*model.Status{stTodo}, nil
	}}
	svc := application.NewStatusService(personalWorkspaces(), ownedProjects(), repo)
	specs := []model.StatusSpec{{ID: stTodo.ID, Name: "Backlog"}, {Name: "Nuevo", Category: model.IssueStateTodo}}
	out, err := svc.BatchUpdateStatuses(context.Background(), owner, application.BatchUpdateStatusesInput{ProjectID: validID, Statuses: specs})
	if err != nil || len(out) != 1 || len(gotSpecs) != 2 || gotActor != owner {
		t.Errorf("batch: out %v specs %v actor %q err %v", out, gotSpecs, gotActor, err)
	}

	failRepo := &mockStatusRepo{batchFn: func(context.Context, string, []model.StatusSpec, []model.StatusDeletion, string) ([]*model.Status, error) {
		t.Fatal("no debería llegar al repo")
		return nil, nil
	}}
	svc = application.NewStatusService(personalWorkspaces(), ownedProjects(), failRepo)
	for name, in := range map[string]application.BatchUpdateStatusesInput{
		"id no UUID":         {Statuses: []model.StatusSpec{{ID: "abc", Name: "X"}}},
		"borrado no UUID":    {Deletions: []model.StatusDeletion{{StatusID: "abc"}}},
		"destino no UUID":    {Deletions: []model.StatusDeletion{{StatusID: stTodo.ID, MoveToStatusID: "abc"}}},
		"borrado sin estado": {Deletions: []model.StatusDeletion{{}}},
	} {
		in.ProjectID = validID
		if _, err := svc.BatchUpdateStatuses(context.Background(), owner, in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}

	boom := errors.New("boom")
	svc = application.NewStatusService(personalWorkspaces(), ownedProjects(), &mockStatusRepo{batchFn: func(context.Context, string, []model.StatusSpec, []model.StatusDeletion, string) ([]*model.Status, error) {
		return nil, boom
	}})
	if _, err := svc.BatchUpdateStatuses(context.Background(), owner, application.BatchUpdateStatusesInput{ProjectID: validID}); !errors.Is(err, boom) {
		t.Errorf("propaga el error del repo: got %v", err)
	}
}

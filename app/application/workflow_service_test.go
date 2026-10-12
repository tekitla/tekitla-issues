package application_test

import (
	"context"
	"slices"
	"testing"

	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// Tests de la rebanada 3a: MoveIssue con estados y orden, UpdateIssue,
// comentarios, historial, StatusService y LabelService.

const (
	labelFront = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	labelBack  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func projectLabels() *mockLabelRepo {
	return &mockLabelRepo{
		listFn: func(context.Context, string) ([]*model.Label, error) {
			return []*model.Label{
				{ID: labelBack, ProjectID: validID, Name: "Backend", Color: model.LabelColorAccent},
				{ID: labelFront, ProjectID: validID, Name: "Frontend", Color: model.LabelColorInfo},
			}, nil
		},
	}
}

func svcWith(issues *mockIssueRepo, statuses *mockStatusRepo, labels *mockLabelRepo) *application.IssueService {
	return application.NewIssueService(personalWorkspaces(), ownedProjects(), issues, statuses, labels)
}

// ================================================================
// MoveIssue: destino y orden
// ================================================================

func TestMoveIssue_ByStatusID(t *testing.T) {
	var gotTarget *model.Status
	var gotPlace port.Placement
	issues := &mockIssueRepo{
		moveFn: func(_ context.Context, _ string, number int32, target *model.Status, place port.Placement, actor string) (*model.Issue, error) {
			gotTarget, gotPlace = target, place
			if actor != owner {
				t.Errorf("actor: got %q", actor)
			}
			return &model.Issue{Number: number, StatusID: target.ID, State: target.Category}, nil
		},
	}
	place := port.Placement{AfterNumber: 4, BeforeNumber: 7}
	_, err := svcWith(issues, projectStatuses(stReview), &mockLabelRepo{}).MoveIssue(context.Background(), owner, application.MoveIssueInput{
		ProjectID: validID, Number: 3, StatusID: stReview.ID, State: model.IssueStateDone, Place: place,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// status_id manda sobre state.
	if gotTarget != stReview || gotPlace != place {
		t.Errorf("move: got target %+v, place %+v", gotTarget, gotPlace)
	}
}

func TestMoveIssue_UnknownStatusIsValidation(t *testing.T) {
	svc := svcWith(failIssueRepo(t), projectStatuses(), &mockLabelRepo{})
	for _, id := range []string{"not-a-uuid", stReview.ID} {
		_, err := svc.MoveIssue(context.Background(), owner, application.MoveIssueInput{ProjectID: validID, Number: 1, StatusID: id})
		if !domainerrors.IsValidation(err) {
			t.Errorf("status_id %q: expected ValidationError, got %v", id, err)
		}
	}
}

// Con solo la categoría: si el issue ya está en un estado de esa categoría no
// cambia de estado (sirve para reordenar dentro de la columna sin conocer los
// estados del proyecto).
func TestMoveIssue_SameCategoryKeepsStatus(t *testing.T) {
	var gotTarget *model.Status
	issues := &mockIssueRepo{
		getByNumberFn: func(_ context.Context, _ string, number int32) (*model.Issue, error) {
			return &model.Issue{Number: number, State: model.IssueStateInProgress, StatusID: stReview.ID}, nil
		},
		moveFn: func(_ context.Context, _ string, number int32, target *model.Status, _ port.Placement, _ string) (*model.Issue, error) {
			gotTarget = target
			return &model.Issue{Number: number}, nil
		},
	}
	_, err := svcWith(issues, projectStatuses(stReview), &mockLabelRepo{}).MoveIssue(context.Background(), owner, application.MoveIssueInput{
		ProjectID: validID, Number: 2, State: model.IssueStateInProgress,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTarget != stReview {
		t.Errorf("target: got %+v, want the current status", gotTarget)
	}
}

func TestMoveIssue_InvalidPlacement(t *testing.T) {
	svc := svcWith(failIssueRepo(t), projectStatuses(), &mockLabelRepo{})
	for name, place := range map[string]port.Placement{
		"negative":   {AfterNumber: -1},
		"itself":     {BeforeNumber: 3},
		"same twice": {AfterNumber: 5, BeforeNumber: 5},
	} {
		_, err := svc.MoveIssue(context.Background(), owner, application.MoveIssueInput{
			ProjectID: validID, Number: 3, StatusID: stDoing.ID, Place: place,
		})
		if !domainerrors.IsValidation(err) {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}
}

func TestMoveIssue_PropagatesStaleOrder(t *testing.T) {
	issues := &mockIssueRepo{
		moveFn: func(context.Context, string, int32, *model.Status, port.Placement, string) (*model.Issue, error) {
			return nil, domainerrors.FailedPreconditionError("neighbors are out of order", nil).WithReason("STALE_ORDER")
		},
	}
	_, err := svcWith(issues, projectStatuses(), &mockLabelRepo{}).MoveIssue(context.Background(), owner, application.MoveIssueInput{
		ProjectID: validID, Number: 3, StatusID: stDoing.ID, Place: port.Placement{AfterNumber: 1},
	})
	if !domainerrors.IsFailedPrecondition(err) {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

// ================================================================
// UpdateIssue
// ================================================================

func ptr[T any](v T) *T { return &v }

func TestUpdateIssue_BuildsNormalizedPatch(t *testing.T) {
	var got model.IssuePatch
	issues := &mockIssueRepo{
		updateFn: func(_ context.Context, _ string, number int32, patch model.IssuePatch, actor string) (*model.Issue, error) {
			got = patch
			if actor != owner {
				t.Errorf("actor: got %q", actor)
			}
			return &model.Issue{Number: number}, nil
		},
	}
	_, err := svcWith(issues, projectStatuses(), projectLabels()).UpdateIssue(context.Background(), owner, application.UpdateIssueInput{
		ProjectID: validID,
		Number:    1,
		Title:     ptr("  Nuevo título "),
		Priority:  ptr(model.PriorityHigh),
		LabelIDs:  &[]string{labelFront, labelBack, labelFront},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *got.Title != "Nuevo título" || got.Description != nil || *got.Priority != model.PriorityHigh {
		t.Errorf("patch: got %+v", got)
	}
	if !slices.Equal(*got.LabelIDs, []string{labelFront, labelBack}) {
		t.Errorf("labels (sin repetidos): got %v", *got.LabelIDs)
	}
}

func TestUpdateIssue_Validation(t *testing.T) {
	svc := svcWith(failIssueRepo(t), projectStatuses(), projectLabels())
	foreign := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	for name, in := range map[string]application.UpdateIssueInput{
		"empty title":      {Title: ptr(" ")},
		"unknown priority": {Priority: ptr(model.Priority(9))},
		"foreign label":    {LabelIDs: &[]string{labelFront, foreign}},
	} {
		in.ProjectID, in.Number = validID, 1
		if _, err := svc.UpdateIssue(context.Background(), owner, in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}
}

func TestUpdateIssue_EmptyPatchReturnsIssue(t *testing.T) {
	issues := &mockIssueRepo{
		getByNumberFn: todoIssue,
		updateFn: func(context.Context, string, int32, model.IssuePatch, string) (*model.Issue, error) {
			t.Fatal("update must not be called without changes")
			return nil, nil
		},
	}
	got, err := svcWith(issues, projectStatuses(), projectLabels()).UpdateIssue(context.Background(), owner, application.UpdateIssueInput{ProjectID: validID, Number: 4})
	if err != nil || got.Number != 4 {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

// Quitar todas las etiquetas es un cambio (lista vacía), no «sin cambios».
func TestUpdateIssue_ClearLabels(t *testing.T) {
	var got model.IssuePatch
	issues := &mockIssueRepo{
		updateFn: func(_ context.Context, _ string, _ int32, patch model.IssuePatch, _ string) (*model.Issue, error) {
			got = patch
			return &model.Issue{}, nil
		},
	}
	labels := &mockLabelRepo{listFn: func(context.Context, string) ([]*model.Label, error) {
		t.Fatal("no labels to validate")
		return nil, nil
	}}
	if _, err := svcWith(issues, projectStatuses(), labels).UpdateIssue(context.Background(), owner, application.UpdateIssueInput{
		ProjectID: validID, Number: 1, LabelIDs: &[]string{},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.LabelIDs == nil || len(*got.LabelIDs) != 0 {
		t.Errorf("labels: got %v", got.LabelIDs)
	}
}

// ================================================================
// Comentarios e historial
// ================================================================

func TestCreateComment(t *testing.T) {
	var got *model.Comment
	issues := &mockIssueRepo{
		addCommentFn: func(_ context.Context, _ string, _ int32, c *model.Comment) (*model.Comment, error) {
			got = c
			return c, nil
		},
	}
	svc := svcWith(issues, projectStatuses(), &mockLabelRepo{})
	if _, err := svc.CreateComment(context.Background(), owner, validID, 2, "  Listo para revisar "); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Body != "Listo para revisar" || got.AuthorUID != owner || got.ID == "" {
		t.Errorf("comment: got %+v", got)
	}
	if _, err := svc.CreateComment(context.Background(), owner, validID, 2, "   "); !domainerrors.IsValidation(err) {
		t.Errorf("empty body: expected ValidationError, got %v", err)
	}
}

func TestListIssueEvents_NormalizesLimit(t *testing.T) {
	var got port.PageFilter
	issues := &mockIssueRepo{
		listEventsFn: func(_ context.Context, _ string, _ int32, page port.PageFilter) (*port.EventPage, error) {
			got = page
			return &port.EventPage{}, nil
		},
	}
	_, err := svcWith(issues, projectStatuses(), &mockLabelRepo{}).ListIssueEvents(context.Background(), owner, validID, 1, port.PageFilter{Cursor: "c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Limit != application.DefaultPageLimit || got.Cursor != "c" {
		t.Errorf("page: got %+v", got)
	}
}

func TestWorkflow_ForeignProjectIsNotFound(t *testing.T) {
	ctx := context.Background()
	issues := svcWith(failIssueRepo(t), &mockStatusRepo{}, &mockLabelRepo{})
	statuses := application.NewStatusService(personalWorkspaces(), ownedProjects(), &mockStatusRepo{})
	labels := application.NewLabelService(personalWorkspaces(), ownedProjects(), &mockLabelRepo{})

	checks := map[string]error{}
	_, checks["UpdateIssue"] = issues.UpdateIssue(ctx, otherOwner, application.UpdateIssueInput{ProjectID: validID, Number: 1, Title: ptr("x")})
	_, checks["CreateComment"] = issues.CreateComment(ctx, otherOwner, validID, 1, "x")
	_, checks["ListIssueEvents"] = issues.ListIssueEvents(ctx, otherOwner, validID, 1, port.PageFilter{})
	_, checks["ListStatuses"] = statuses.ListStatuses(ctx, otherOwner, validID)
	checks["DeleteStatus"] = statuses.DeleteStatus(ctx, otherOwner, validID, stDoing.ID, "")
	_, checks["ListLabels"] = labels.ListLabels(ctx, otherOwner, validID)
	checks["DeleteLabel"] = labels.DeleteLabel(ctx, otherOwner, validID, labelFront)
	for name, err := range checks {
		if !domainerrors.IsNotFound(err) {
			t.Errorf("%s: expected NotFound, got %v", name, err)
		}
	}
}

// ================================================================
// StatusService
// ================================================================

func TestCreateStatus(t *testing.T) {
	var got *model.Status
	var gotAfter string
	repo := &mockStatusRepo{
		createFn: func(_ context.Context, s *model.Status, afterID string) (*model.Status, error) {
			got, gotAfter = s, afterID
			return s, nil
		},
	}
	svc := application.NewStatusService(personalWorkspaces(), ownedProjects(), repo)
	_, err := svc.CreateStatus(context.Background(), owner, application.CreateStatusInput{
		ProjectID: validID, Name: " En revisión ", Category: model.IssueStateInProgress, AfterStatusID: stDoing.ID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "En revisión" || got.Category != model.IssueStateInProgress || got.ProjectID != validID || got.ID == "" || gotAfter != stDoing.ID {
		t.Errorf("status: got %+v after %q", got, gotAfter)
	}
}

func TestCreateStatus_Validation(t *testing.T) {
	svc := application.NewStatusService(personalWorkspaces(), ownedProjects(), &mockStatusRepo{
		createFn: func(context.Context, *model.Status, string) (*model.Status, error) {
			t.Fatal("repo must not be called")
			return nil, nil
		},
	})
	for name, in := range map[string]application.CreateStatusInput{
		"empty name":       {Name: " ", Category: model.IssueStateTodo},
		"no category":      {Name: "x"},
		"unknown category": {Name: "x", Category: "blocked"},
		"bad after":        {Name: "x", Category: model.IssueStateTodo, AfterStatusID: "nope"},
	} {
		in.ProjectID = validID
		if _, err := svc.CreateStatus(context.Background(), owner, in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}
}

func TestUpdateStatus(t *testing.T) {
	var gotName, gotAfter *string
	repo := &mockStatusRepo{
		updateFn: func(_ context.Context, _, id string, patch port.StatusPatch) (*model.Status, error) {
			gotName, gotAfter = patch.Name, patch.AfterID
			return &model.Status{ID: id}, nil
		},
		getFn: func(_ context.Context, _, id string) (*model.Status, error) {
			return &model.Status{ID: id, Name: "same"}, nil
		},
	}
	svc := application.NewStatusService(personalWorkspaces(), ownedProjects(), repo)

	// Al principio: after presente y vacío.
	if _, err := svc.UpdateStatus(context.Background(), owner, application.UpdateStatusInput{
		ProjectID: validID, StatusID: stDone.ID, Name: ptr(" Terminado "), AfterStatusID: ptr(""),
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *gotName != "Terminado" || gotAfter == nil || *gotAfter != "" {
		t.Errorf("update: name %v after %v", gotName, gotAfter)
	}

	// Sin cambios: lo devuelve tal cual.
	got, err := svc.UpdateStatus(context.Background(), owner, application.UpdateStatusInput{ProjectID: validID, StatusID: stDone.ID})
	if err != nil || got.Name != "same" {
		t.Errorf("no changes: got %+v, err %v", got, err)
	}

	// Después de sí mismo: inválido.
	_, err = svc.UpdateStatus(context.Background(), owner, application.UpdateStatusInput{ProjectID: validID, StatusID: stDone.ID, AfterStatusID: ptr(stDone.ID)})
	if !domainerrors.IsValidation(err) {
		t.Errorf("after itself: expected ValidationError, got %v", err)
	}
}

func TestDeleteStatus(t *testing.T) {
	var gotMove, gotActor string
	repo := &mockStatusRepo{
		deleteFn: func(_ context.Context, _, _, moveToID, actor string) error {
			gotMove, gotActor = moveToID, actor
			return nil
		},
	}
	svc := application.NewStatusService(personalWorkspaces(), ownedProjects(), repo)
	if err := svc.DeleteStatus(context.Background(), owner, validID, stReview.ID, stDoing.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMove != stDoing.ID || gotActor != owner {
		t.Errorf("delete: move %q actor %q", gotMove, gotActor)
	}
	if err := svc.DeleteStatus(context.Background(), owner, validID, stReview.ID, stReview.ID); !domainerrors.IsValidation(err) {
		t.Errorf("move to itself: expected ValidationError, got %v", err)
	}
}

func TestDeleteStatus_PropagatesLastOfCategory(t *testing.T) {
	repo := &mockStatusRepo{
		deleteFn: func(context.Context, string, string, string, string) error {
			return domainerrors.FailedPreconditionError("cannot delete the last status of its category", nil)
		},
	}
	err := application.NewStatusService(personalWorkspaces(), ownedProjects(), repo).DeleteStatus(context.Background(), owner, validID, stTodo.ID, "")
	if !domainerrors.IsFailedPrecondition(err) {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

// ================================================================
// LabelService
// ================================================================

func TestCreateLabel_DefaultsColor(t *testing.T) {
	var got *model.Label
	repo := &mockLabelRepo{createFn: func(_ context.Context, l *model.Label) (*model.Label, error) { got = l; return l, nil }}
	svc := application.NewLabelService(personalWorkspaces(), ownedProjects(), repo)
	if _, err := svc.CreateLabel(context.Background(), owner, validID, " Frontend ", model.LabelColorUnspecified); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "Frontend" || got.Color != model.LabelColorNeutral || got.ProjectID != validID || got.ID == "" {
		t.Errorf("label: got %+v", got)
	}
	if _, err := svc.CreateLabel(context.Background(), owner, validID, "x", model.LabelColor(42)); !domainerrors.IsValidation(err) {
		t.Errorf("unknown color: expected ValidationError, got %v", err)
	}
}

func TestUpdateLabel(t *testing.T) {
	var gotName *string
	var gotColor *model.LabelColor
	repo := &mockLabelRepo{updateFn: func(_ context.Context, _, id string, name *string, color *model.LabelColor) (*model.Label, error) {
		gotName, gotColor = name, color
		return &model.Label{ID: id}, nil
	}}
	svc := application.NewLabelService(personalWorkspaces(), ownedProjects(), repo)
	if _, err := svc.UpdateLabel(context.Background(), owner, validID, labelFront, nil, ptr(model.LabelColorBrand)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotName != nil || *gotColor != model.LabelColorBrand {
		t.Errorf("update: name %v color %v", gotName, gotColor)
	}
	if _, err := svc.UpdateLabel(context.Background(), owner, validID, "nope", ptr("x"), nil); !domainerrors.IsValidation(err) {
		t.Errorf("bad label_id: expected ValidationError, got %v", err)
	}
}

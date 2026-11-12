package application_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

func importedEvent(ref string) model.ImportedEvent {
	return model.ImportedEvent{
		SourceRef: ref, Source: model.EventSourceImportHistory, OccurredAt: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
		Status: &model.StatusChange{FromName: "open", FromCategory: model.IssueStateTodo, ToName: "in-progress", ToCategory: model.IssueStateInProgress},
	}
}

// ImportIssueEvents (issue 036): normaliza y valida antes de llegar al repo.
func TestIssueService_ImportIssueEvents(t *testing.T) {
	ctx := context.Background()
	var gotKey string
	var gotEvents []model.ImportedEvent
	var gotOverwrite bool
	issues := &mockIssueRepo{importEvtsFn: func(_ context.Context, _ []string, _, sourceKey string, events []model.ImportedEvent, overwrite bool, _ model.Author) (model.ImportEventsResult, error) {
		gotKey, gotEvents, gotOverwrite = sourceKey, events, overwrite
		return model.ImportEventsResult{Created: len(events)}, nil
	}}
	svc := application.NewIssueService(personalWorkspaces(), ownedProjects(), issues, projectStatuses(), &mockLabelRepo{}, &mockActorRepo{})

	ev := importedEvent(" abc123 ")
	ev.Status.ToName = " in-progress "
	res, err := svc.ImportIssueEvents(ctx, as(owner), application.ImportIssueEventsInput{
		ProjectID: validID, SourceKey: " 007-cola ", Events: []model.ImportedEvent{ev, importedEvent("inicial")}, Overwrite: true,
	})
	if err != nil || res.Created != 2 {
		t.Fatalf("ImportIssueEvents: %+v %v", res, err)
	}
	if gotKey != "007-cola" || !gotOverwrite || len(gotEvents) != 2 || gotEvents[0].SourceRef != "abc123" || gotEvents[0].Status.ToName != "in-progress" {
		t.Errorf("repo got key %q overwrite %v events %+v", gotKey, gotOverwrite, gotEvents)
	}

	with := func(change func(e *model.ImportedEvent)) []model.ImportedEvent {
		e := importedEvent("x")
		change(&e)
		return []model.ImportedEvent{e}
	}
	many := make([]model.ImportedEvent, model.ImportEventsMax+1)
	for i := range many {
		many[i] = importedEvent(fmt.Sprintf("r%d", i))
	}
	for name, events := range map[string][]model.ImportedEvent{
		"none":         nil,
		"too many":     many,
		"repeated ref": {importedEvent("a"), importedEvent("a")},
		"empty ref":    with(func(e *model.ImportedEvent) { e.SourceRef = " " }),
		"long ref":     with(func(e *model.ImportedEvent) { e.SourceRef = strings.Repeat("r", 201) }),
		"live source":  with(func(e *model.ImportedEvent) { e.Source = model.EventSourceLive }),
		"sync source":  with(func(e *model.ImportedEvent) { e.Source = model.EventSourceImportSync }),
		"no source":    with(func(e *model.ImportedEvent) { e.Source = "" }),
		"no date":      with(func(e *model.ImportedEvent) { e.OccurredAt = time.Time{} }),
		"future":       with(func(e *model.ImportedEvent) { e.OccurredAt = time.Now().Add(72 * time.Hour) }),
		"no change":    with(func(e *model.ImportedEvent) { e.Status = nil }),
		"no state":     with(func(e *model.ImportedEvent) { e.Status.ToCategory = model.IssueStateUnspecified }),
		"empty name":   with(func(e *model.ImportedEvent) { e.Status.FromName = "" }),
		"long name":    with(func(e *model.ImportedEvent) { e.Status.ToName = strings.Repeat("n", 41) }),
		"no-op":        with(func(e *model.ImportedEvent) { e.Status.ToName, e.Status.ToCategory = "open", model.IssueStateTodo }),
	} {
		_, err := svc.ImportIssueEvents(ctx, as(owner), application.ImportIssueEventsInput{ProjectID: validID, SourceKey: "k", Events: events})
		if !domainerrors.IsValidation(err) {
			t.Errorf("%s: want validation, got %v", name, err)
		}
	}
	if _, err := svc.ImportIssueEvents(ctx, as(owner), application.ImportIssueEventsInput{ProjectID: validID, SourceKey: " ", Events: []model.ImportedEvent{importedEvent("a")}}); !domainerrors.IsValidation(err) {
		t.Errorf("no source key: want validation, got %v", err)
	}
	if _, err := svc.ImportIssueEvents(ctx, as(otherOwner), application.ImportIssueEventsInput{ProjectID: validID, SourceKey: "k", Events: []model.ImportedEvent{importedEvent("a")}}); !domainerrors.IsNotFound(err) {
		t.Errorf("foreign project: want not found, got %v", err)
	}
}

// override_created_at sin created_at es inválido; con created_at llega al repo.
func TestIssueService_ImportIssue_OverrideCreatedAt(t *testing.T) {
	ctx := context.Background()
	var got model.ImportSpec
	issues := &mockIssueRepo{importFn: func(_ context.Context, _ []string, spec model.ImportSpec, _ model.Author) (*model.Issue, model.ImportOutcome, error) {
		got = spec
		return &model.Issue{}, model.ImportUpdated, nil
	}}
	svc := application.NewIssueService(personalWorkspaces(), ownedProjects(), issues, projectStatuses(), &mockLabelRepo{}, &mockActorRepo{})
	in := application.ImportIssueInput{ProjectID: validID, SourceKey: "k", Number: 1, Title: "x", State: model.IssueStateTodo, OverrideCreatedAt: true}
	if _, _, err := svc.ImportIssue(ctx, as(owner), in); !domainerrors.IsValidation(err) {
		t.Errorf("override without created_at: want validation, got %v", err)
	}
	in.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := svc.ImportIssue(ctx, as(owner), in); err != nil || !got.OverrideCreatedAt || !got.CreatedAt.Equal(in.CreatedAt) {
		t.Errorf("override: %v %+v", err, got)
	}
}

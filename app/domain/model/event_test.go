package model_test

import (
	"testing"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

func ptr[T any](v T) *T { return &v }

func TestIssueChanges_OnlyRealChanges(t *testing.T) {
	issue := &model.Issue{Title: "A", Description: "d", Priority: model.PriorityLow, LabelIDs: []string{"l1", "l2"}}
	labels := map[string]*model.Label{
		"l1": {ID: "l1", Name: "Uno"},
		"l2": {ID: "l2", Name: "Dos"},
		"l3": {ID: "l3", Name: "Tres"},
	}

	// Mismos valores: sin eventos.
	same := model.IssuePatch{Title: ptr("A"), Description: ptr("d"), Priority: ptr(model.PriorityLow), LabelIDs: &[]string{"l2", "l1"}}
	if events := model.IssueChanges(issue, same, labels); len(events) != 0 {
		t.Fatalf("same values: got %d events", len(events))
	}

	events := model.IssueChanges(issue, model.IssuePatch{
		Title:       ptr("B"),
		Description: ptr("otra"),
		Priority:    ptr(model.PriorityUrgent),
		LabelIDs:    &[]string{"l2", "l3"},
	}, labels)
	var kinds []model.EventKind
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	want := []model.EventKind{model.EventTitleChanged, model.EventDescriptionChanged, model.EventPriorityChanged, model.EventLabelsChanged}
	if len(kinds) != len(want) {
		t.Fatalf("kinds: got %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds: got %v, want %v", kinds, want)
		}
	}
	if c := events[0].Title; c.From != "A" || c.To != "B" {
		t.Errorf("title: got %+v", c)
	}
	if c := events[2].Priority; c.From != model.PriorityLow || c.To != model.PriorityUrgent {
		t.Errorf("priority: got %+v", c)
	}
	lc := events[3].Labels
	if len(lc.Added) != 1 || lc.Added[0].Name != "Tres" || len(lc.Removed) != 1 || lc.Removed[0].Name != "Uno" {
		t.Errorf("labels: added %+v removed %+v", lc.Added, lc.Removed)
	}
}

func TestFirstStatusOf(t *testing.T) {
	statuses := []*model.Status{
		{ID: "todo", Category: model.IssueStateTodo},
		{ID: "doing", Category: model.IssueStateInProgress},
		{ID: "review", Category: model.IssueStateInProgress},
	}
	if s := model.FirstStatusOf(statuses, model.IssueStateInProgress); s == nil || s.ID != "doing" {
		t.Errorf("in progress: got %+v", s)
	}
	if s := model.FirstStatusOf(statuses, model.IssueStateDone); s != nil {
		t.Errorf("done: got %+v, want nil", s)
	}
}

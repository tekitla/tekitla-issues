package postgres

import (
	"testing"

	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

func TestIssueFilterFingerprint(t *testing.T) {
	base := port.IssueFilter{
		ProjectID:  cursorTestID,
		States:     []model.IssueState{model.IssueStateTodo, model.IssueStateDone},
		LabelIDs:   []string{"b", "a"},
		Priorities: []model.Priority{model.PriorityHigh, model.PriorityLow},
		Order:      model.IssueOrderRank,
		Cursor:     "x",
		Limit:      10,
	}
	same := base
	same.States = []model.IssueState{model.IssueStateDone, model.IssueStateTodo}
	same.LabelIDs = []string{"a", "b"}
	same.Priorities = []model.Priority{model.PriorityLow, model.PriorityHigh}
	same.Cursor, same.Limit = "", 50
	if issueFilterFingerprint(base) != issueFilterFingerprint(same) {
		t.Error("la huella depende del orden de las listas, del cursor o del límite")
	}
	for name, change := range map[string]func(*port.IssueFilter){
		"orden":    func(f *port.IssueFilter) { f.Order = model.IssueOrderUpdatedDesc },
		"búsqueda": func(f *port.IssueFilter) { f.Search = model.IssueSearch{Text: "x"} },
		"estados":  func(f *port.IssueFilter) { f.States = nil },
		"proyecto": func(f *port.IssueFilter) { f.ProjectID = "otro" },
		"status":   func(f *port.IssueFilter) { f.StatusIDs = []string{"s"} },
	} {
		other := base
		change(&other)
		if issueFilterFingerprint(base) == issueFilterFingerprint(other) {
			t.Errorf("%s: la huella no cambia", name)
		}
	}
}

func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`50% de a_b\c`); got != `50\% de a\_b\\c` {
		t.Errorf("escapeLike: got %q", got)
	}
}

func TestEventDataDiscardedAndRestored(t *testing.T) {
	change := &model.StatusChange{FromID: "f", FromName: "Por hacer", FromCategory: model.IssueStateTodo,
		ToID: "t", ToName: "Descartado", ToCategory: model.IssueStateCanceled}
	for _, ev := range []*model.IssueEvent{
		{Kind: model.EventDiscarded, Status: change, Reason: "duplicado"},
		{Kind: model.EventRestored, Status: change},
	} {
		data, err := eventData(ev)
		if err != nil {
			t.Fatalf("%s: %v", ev.Kind, err)
		}
		got, err := fromEventRow(&eventRow{ID: "e", Kind: string(ev.Kind), Data: data})
		if err != nil {
			t.Fatalf("%s: %v", ev.Kind, err)
		}
		if *got.Status != *change || got.Reason != ev.Reason {
			t.Errorf("%s: got %+v reason %q (data %s)", ev.Kind, got.Status, got.Reason, data)
		}
	}
}

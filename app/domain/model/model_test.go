package model_test

import (
	"strings"
	"testing"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

func TestNormalizeProjectKey_Valid(t *testing.T) {
	cases := map[string]string{
		"TK":         "TK",
		"tk":         "TK",
		"  Ab1  ":    "AB1",
		"A1":         "A1",
		"ABCDEFGHIJ": "ABCDEFGHIJ",
	}
	for in, want := range cases {
		got, err := model.NormalizeProjectKey(in)
		if err != nil {
			t.Errorf("NormalizeProjectKey(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeProjectKey(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeProjectKey_Invalid(t *testing.T) {
	for _, in := range []string{
		"",            // vacía
		"   ",         // solo espacios
		"T",           // muy corta
		"ABCDEFGHIJK", // 11
		"1A",          // empieza con dígito
		"T-Q",         // guion
		"T Q",         // espacio interno
		"TÑ",          // no ASCII
		"T_Q",         // guion bajo
	} {
		if _, err := model.NormalizeProjectKey(in); !domainerrors.IsValidation(err) {
			t.Errorf("NormalizeProjectKey(%q): expected ValidationError, got %v", in, err)
		}
	}
}

func TestNormalizeProjectName(t *testing.T) {
	if got, err := model.NormalizeProjectName("  Tekitla "); err != nil || got != "Tekitla" {
		t.Errorf("trim: got %q, %v", got, err)
	}
	if _, err := model.NormalizeProjectName(" "); !domainerrors.IsValidation(err) {
		t.Errorf("empty: expected ValidationError, got %v", err)
	}
	if _, err := model.NormalizeProjectName(strings.Repeat("ñ", model.ProjectNameMaxLen)); err != nil {
		t.Errorf("max len in runes must be valid, got %v", err)
	}
	if _, err := model.NormalizeProjectName(strings.Repeat("a", model.ProjectNameMaxLen+1)); !domainerrors.IsValidation(err) {
		t.Errorf("too long: expected ValidationError, got %v", err)
	}
}

func TestNormalizeIssueTitle(t *testing.T) {
	if got, err := model.NormalizeIssueTitle("  Arreglar login \n"); err != nil || got != "Arreglar login" {
		t.Errorf("trim: got %q, %v", got, err)
	}
	if _, err := model.NormalizeIssueTitle("\t"); !domainerrors.IsValidation(err) {
		t.Errorf("empty: expected ValidationError, got %v", err)
	}
	if _, err := model.NormalizeIssueTitle(strings.Repeat("a", model.IssueTitleMaxLen+1)); !domainerrors.IsValidation(err) {
		t.Errorf("too long: expected ValidationError, got %v", err)
	}
}

func TestNormalizeDescriptions(t *testing.T) {
	if got, err := model.NormalizeIssueDescription(""); err != nil || got != "" {
		t.Errorf("empty description must be valid: got %q, %v", got, err)
	}
	if _, err := model.NormalizeIssueDescription(strings.Repeat("a", model.IssueDescriptionMaxLen+1)); !domainerrors.IsValidation(err) {
		t.Errorf("issue description too long: expected ValidationError, got %v", err)
	}
	if _, err := model.NormalizeProjectDescription(strings.Repeat("a", model.ProjectDescriptionMaxLen+1)); !domainerrors.IsValidation(err) {
		t.Errorf("project description too long: expected ValidationError, got %v", err)
	}
}

func TestIssueState_IsValid(t *testing.T) {
	for _, s := range []model.IssueState{model.IssueStateTodo, model.IssueStateInProgress, model.IssueStateDone, model.IssueStateCanceled} {
		if !s.IsValid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []model.IssueState{model.IssueStateUnspecified, "blocked", "TODO"} {
		if s.IsValid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestPriority_IsValid(t *testing.T) {
	for p := model.PriorityLow; p <= model.PriorityUrgent; p++ {
		if !p.IsValid() {
			t.Errorf("%d should be valid", p)
		}
	}
	for _, p := range []model.Priority{model.PriorityUnspecified, -1, 5} {
		if p.IsValid() {
			t.Errorf("%d should be invalid", p)
		}
	}
	if !model.DefaultPriority.IsValid() || model.DefaultPriority != model.PriorityMedium {
		t.Errorf("DefaultPriority must be Medium, got %d", model.DefaultPriority)
	}
}

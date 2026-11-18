package model_test

import (
	"strconv"
	"testing"
	"time"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

func TestNormalizeCwdRel(t *testing.T) {
	ok := map[string]string{
		"":                             "",
		".":                            ".",
		"round/worktrees/issue-231/fe": "round/worktrees/issue-231/fe",
		`tekitla\worktrees\issue-25`:   "tekitla/worktrees/issue-25",
		"a/..b/c":                      "a/..b/c",
	}
	for in, want := range ok {
		got, err := model.NormalizeCwdRel(in)
		if err != nil || got != want {
			t.Errorf("NormalizeCwdRel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// CU-026-9: ni rutas absolutas ni salir de la raíz.
	for _, in := range []string{"/home/x", `\\server\share`, `C:\dev`, "c:/dev", "a/../b", ".."} {
		if _, err := model.NormalizeCwdRel(in); !domainerrors.IsValidation(err) {
			t.Errorf("NormalizeCwdRel(%q): want ValidationError, got %v", in, err)
		}
	}
}

func TestNormalizeReport_WaitingRules(t *testing.T) {
	in := []model.ReportedSession{
		// CU-026-12: trabajando con espera vieja → NONE.
		{ExternalID: "a", Tool: model.AgentToolClaudeCode, Link: model.SessionLinkNone, Status: model.SessionWorking,
			WaitingOn: model.WaitingSession, WaitingTarget: "x", WaitingText: "⏳"},
		// PERSON no lleva target.
		{ExternalID: "b", Tool: model.AgentToolClaudeCode, Link: model.SessionLinkNone, Status: model.SessionWaitingOK,
			WaitingOn: model.WaitingPerson, WaitingTarget: "x", WaitingText: "🙋"},
		// Sin waiting_on = NONE.
		{ExternalID: "c", Tool: model.AgentToolCodex, Link: model.SessionLinkCwd, ProjectKey: " tk ", IssueNumber: 26, Status: model.SessionIdle},
	}
	out, err := model.NormalizeReport(model.SessionSourceHerdr, in)
	if err != nil {
		t.Fatalf("NormalizeReport: %v", err)
	}
	if out[0].WaitingOn != model.WaitingNone || out[0].WaitingTarget != "" || out[0].WaitingText != "" {
		t.Errorf("working: %+v", out[0])
	}
	if out[1].WaitingOn != model.WaitingPerson || out[1].WaitingTarget != "" || out[1].WaitingText != "🙋" {
		t.Errorf("person: %+v", out[1])
	}
	if out[2].WaitingOn != model.WaitingNone || out[2].ProjectKey != "TK" {
		t.Errorf("idle: %+v", out[2])
	}
	if _, err := model.NormalizeReport(model.SessionSourceHerdr, make([]model.ReportedSession, model.ReportMaxSessions+1)); !domainerrors.IsValidation(err) {
		t.Errorf("too many: %v", err)
	}
}

func TestPlanReport(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	key := model.SessionKey{OwnerActorID: "p", Source: model.SessionSourceHerdr, Machine: "PC-01"}
	agents := map[model.AgentTool]model.SessionAgent{model.AgentToolClaudeCode: {ID: "ag", DisplayName: "Claude Code PC-01"}}
	ended := t0.Add(-time.Hour)
	existing := []*model.AgentSession{
		{ID: "1", ExternalID: "same", AgentActorID: "ag", Tool: model.AgentToolClaudeCode, Link: model.SessionLinkNone,
			Status: model.SessionIdle, WaitingOn: model.WaitingNone, StartedAt: t0, StatusChangedAt: t0, LastSeenAt: t0},
		{ID: "2", ExternalID: "changes", AgentActorID: "ag", Tool: model.AgentToolClaudeCode, Link: model.SessionLinkNone,
			Status: model.SessionWorking, WaitingOn: model.WaitingNone, StartedAt: t0, StatusChangedAt: t0, LastSeenAt: t0},
		{ID: "3", ExternalID: "gone", Status: model.SessionWorking, LastSeenAt: t0},
		{ID: "4", ExternalID: "reopens", AgentActorID: "ag", Tool: model.AgentToolClaudeCode, Status: model.SessionEnded, EndedAt: &ended, StartedAt: t0.Add(-2 * time.Hour)},
		{ID: "5", ExternalID: "old-ended", Status: model.SessionEnded, EndedAt: &ended},
	}
	reported := []model.ReportedSession{
		{ExternalID: "same", Tool: model.AgentToolClaudeCode, Link: model.SessionLinkNone, Status: model.SessionIdle, WaitingOn: model.WaitingNone},
		{ExternalID: "changes", Tool: model.AgentToolClaudeCode, Link: model.SessionLinkNone, Status: model.SessionWaitingOK, WaitingOn: model.WaitingPerson},
		{ExternalID: "reopens", Tool: model.AgentToolClaudeCode, Link: model.SessionLinkNone, Status: model.SessionWorking, WaitingOn: model.WaitingNone},
		{ExternalID: "new", Tool: model.AgentToolClaudeCode, Link: model.SessionLinkCwd, ProjectKey: "XYZ", IssueNumber: 3, Status: model.SessionWorking, WaitingOn: model.WaitingNone},
		// Herramienta sin agente (rechazada por el servicio): se saltea.
		{ExternalID: "skipped", Tool: model.AgentToolCodex, Link: model.SessionLinkNone, Status: model.SessionWorking, WaitingOn: model.WaitingNone},
	}
	n := 0
	out := model.PlanReport(key, existing, reported, agents, t1, func() string { n++; return "new-" + strconv.Itoa(n) })
	if out.Created != 1 || out.Updated != 2 || out.Unchanged != 1 || out.Ended != 1 || len(out.EndedIDs) != 1 || out.EndedIDs[0] != "3" {
		t.Fatalf("counts: %+v", out)
	}
	by := map[string]*model.AgentSession{}
	for _, u := range out.Upserts {
		by[u.ExternalID] = u
	}
	if s := by["same"]; s.StatusChangedAt != t0 || s.LastSeenAt != t1 || s.StartedAt != t0 {
		t.Errorf("same: %+v", s)
	}
	if s := by["changes"]; s.StatusChangedAt != t1 || s.ID != "2" {
		t.Errorf("changes: %+v", s)
	}
	if s := by["reopens"]; s.ID != "4" || s.StartedAt != t1 || s.EndedAt != nil {
		t.Errorf("reopens: %+v", s)
	}
	// Clave sin resolver (ProjectID vacío): sin issue.
	if s := by["new"]; s.ID != "new-1" || s.Link != model.SessionLinkNone || s.IssueNumber != 0 || s.AgentDisplayName != "Claude Code PC-01" {
		t.Errorf("new: %+v", s)
	}
	if _, ok := by["skipped"]; ok {
		t.Error("skipped tool was planned")
	}
}

func TestSummarizeSessions(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	stall := 10 * time.Minute
	mk := func(id string, st model.SessionStatus, w model.WaitingOn, seenAgo time.Duration) *model.AgentSession {
		return &model.AgentSession{ID: id, Status: st, WaitingOn: w, LastSeenAt: now.Add(-seenAgo)}
	}
	list := []*model.AgentSession{
		mk("w1", model.SessionWorking, model.WaitingNone, time.Minute),
		mk("stalled", model.SessionWorking, model.WaitingNone, 11*time.Minute),
		mk("idle", model.SessionIdle, model.WaitingNone, 2*time.Minute),
		mk("w2", model.SessionWorking, model.WaitingNone, 0),
		mk("person", model.SessionWaitingOK, model.WaitingPerson, time.Hour),
		mk("other", model.SessionWaitingOK, model.WaitingOther, 3*time.Minute),
		mk("old", model.SessionIdle, model.WaitingNone, 25*time.Hour),
		mk("ended", model.SessionEnded, model.WaitingNone, 0),
	}
	sum := model.SummarizeSessions(list, now, stall, true)
	if sum.Active != 6 || sum.Working != 2 || sum.Stalled != 1 || sum.Idle != 1 || sum.Waiting != 2 || !sum.NeedsPerson {
		t.Fatalf("counts: %+v", sum)
	}
	var ids []string
	for _, s := range sum.Items {
		ids = append(ids, s.ID)
	}
	want := []string{"person", "stalled", "w2", "w1", "idle"}
	if len(ids) != model.IssueSessionsMaxItems || strconv.Quote(joinIDs(ids)) != strconv.Quote(joinIDs(want)) {
		t.Errorf("items order: got %v, want %v", ids, want)
	}
	if sum.Items[1].Status != model.SessionStalled || list[1].Status != model.SessionWorking {
		t.Error("items must carry the effective status without touching the input")
	}
	if got := model.SummarizeSessions(list[6:], now, stall, true); got != nil {
		t.Errorf("no active: got %+v", got)
	}
	if got := model.SummarizeSessions(list, now, stall, false); len(got.Items) != 0 {
		t.Error("items without withItems")
	}
}

func joinIDs(ids []string) string {
	s := ""
	for _, id := range ids {
		s += id + ","
	}
	return s
}

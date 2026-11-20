package postgres_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/infra/postgres"
)

// Test del repo de sesiones (issues 026 y 036) contra un Postgres de verdad,
// como el de la integración del 016: se salta sin TEKITLA_PG_TEST_URL, que
// apunta a una base descartable (scripts/test_pg.sh la levanta en docker y la
// borra al terminar; el job `test` del CI usa un service). Nunca contra QA
// ni prod. Cada corrida usa ids nuevos. Datos inventados.

// Issue 036 (incidente del 2026-11-20 en prod): ReportSessions con dos o más
// sesiones guardadas fallaba con «cannot cast type record to uuid» al buscar
// cuáles ya tenían historia. Con una sola sesión andaba, y así se había
// probado a mano.
func TestAgentSessionsReport_Postgres(t *testing.T) {
	db := pgTestDB(t)
	repo := postgres.NewAgentSessionRepository(db)
	ctx := context.Background()

	person, agent, ws, project := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	uid := "it-" + person[:8]
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO issues.actors (id, kind, user_uid, display_name, created_at) VALUES (?, 'person', ?, '', ?)`, []any{person, uid, now}},
		{`INSERT INTO issues.actors (id, kind, owner_actor_id, tool, display_name, created_at) VALUES (?, 'agent', ?, 'claude_code', ?, ?)`, []any{agent, person, "Claude Code IT-" + person[:4], now}},
		{`INSERT INTO issues.workspaces (id, owner_uid) VALUES (?, ?)`, []any{ws, uid}},
		{`INSERT INTO issues.projects (id, workspace_id, key, name, description, next_issue_number) VALUES (?, ?, 'IT', 'Integración', '', 1)`, []any{project, ws}},
	} {
		if err := db.Exec(q.sql, q.args...).Error; err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	key := model.SessionKey{OwnerActorID: person, Source: model.SessionSourceHerdr, Machine: "IT-01"}
	agents := map[model.AgentTool]model.SessionAgent{model.AgentToolClaudeCode: {ID: agent, DisplayName: "Claude Code IT"}}
	session := func(ext string, st model.SessionStatus, w model.WaitingOn, r model.WaitingReason, n int32) model.ReportedSession {
		return model.ReportedSession{
			ExternalID: ext, Tool: model.AgentToolClaudeCode, ProjectKey: "IT", ProjectID: project, IssueNumber: n,
			Link: model.SessionLinkCwd, Status: st, WaitingOn: w, WaitingReason: r,
		}
	}
	report := func(at time.Time, reported ...model.ReportedSession) model.ReportOutcome {
		t.Helper()
		out, err := repo.Report(ctx, key, at, func(existing []*model.AgentSession, history map[string]bool) model.ReportOutcome {
			o := model.PlanReport(key, existing, reported, agents, at, uuid.NewString)
			o.Transitions = model.PlanTransitions(existing, history, o, at, 10*time.Minute)
			return o
		})
		if err != nil {
			var de *domainerrors.DomainError
			if errors.As(err, &de) {
				t.Fatalf("Report at %s: %v %v", at.Format(time.TimeOnly), err, de.Details)
			}
			t.Fatalf("Report at %s: %v", at.Format(time.TimeOnly), err)
		}
		return out
	}

	t0 := now.Add(-time.Hour)
	// 1. Dos sesiones nuevas.
	if out := report(t0, session("a", model.SessionWorking, model.WaitingNone, "", 1), session("b", model.SessionIdle, model.WaitingNone, "", 2)); out.Created != 2 || len(out.Transitions) != 2 {
		t.Fatalf("first report: %+v", out)
	}
	// 2. Latido: dos sesiones con historia, nada cambia (el caso que fallaba).
	if out := report(t0.Add(2*time.Minute), session("a", model.SessionWorking, model.WaitingNone, "", 1), session("b", model.SessionIdle, model.WaitingNone, "", 2)); out.Unchanged != 2 || len(out.Transitions) != 0 {
		t.Fatalf("heartbeat: %+v", out)
	}
	// 3. Cambio con motivo (esperando-ok sin token conserva el motivo), y un
	// hueco de más de 10 minutos en la otra.
	t3 := t0.Add(20 * time.Minute)
	out := report(t3, session("a", model.SessionWaitingOK, model.WaitingNone, model.WaitingReasonReview, 1), session("b", model.SessionIdle, model.WaitingNone, "", 2))
	if len(out.Transitions) != 4 { // a: unreported + nuevo estado; b: unreported + mismo estado
		t.Fatalf("change + gap: %d transitions: %+v", len(out.Transitions), out.Transitions)
	}
	// 4. «b» desaparece del reporte: termina.
	if out := report(t3.Add(time.Minute), session("a", model.SessionWaitingOK, model.WaitingNone, model.WaitingReasonReview, 1)); out.Ended != 1 {
		t.Fatalf("end: %+v", out)
	}

	flow := postgres.NewFlowRepository(db)
	tracks, err := flow.ListSessionTracks(ctx, project, 0, now)
	if err != nil {
		t.Fatalf("ListSessionTracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks: %d", len(tracks))
	}
	byLen := map[int]bool{}
	var reason model.WaitingReason
	for _, tr := range tracks {
		byLen[len(tr.Transitions)] = true
		for _, x := range tr.Transitions {
			if x.WaitingReason != "" {
				reason = x.WaitingReason
			}
		}
	}
	// a: alta, unreported, waiting_ok · b: alta, unreported, idle, ended.
	if !byLen[3] || !byLen[4] || reason != model.WaitingReasonReview {
		t.Errorf("history: lengths %v, reason %q", byLen, reason)
	}
	// Solo un issue: el filtro por número.
	one, err := flow.ListSessionTracks(ctx, project, 2, now)
	if err != nil || len(one) != 1 {
		t.Errorf("ListSessionTracks issue 2: %d, %v", len(one), err)
	}

	// Muchas sesiones a la vez (como una máquina con 30 paneles).
	var many []model.ReportedSession
	for i := 0; i < 30; i++ {
		many = append(many, session("m"+strconv.Itoa(i), model.SessionIdle, model.WaitingNone, "", 3))
	}
	report(now.Add(-5*time.Minute), many...)
	report(now.Add(-4*time.Minute), many...)
}

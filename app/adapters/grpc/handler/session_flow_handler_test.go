package handler_test

import (
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

// Issue 036, parte 3: el recolector reporta, ReportSessions guarda la
// historia y GetIssueFlowEfficiency / GetFlowMetrics la leen. Datos
// inventados.

// heartbeat reporta s cada 2 minutos durante d (el latido del recolector):
// así no hay huecos de más de 10 minutos.
func heartbeat(t *testing.T, c clients, s *issuesv1.ReportedSession, d time.Duration) {
	t.Helper()
	ctx := asUser(t, "u1")
	for elapsed := time.Duration(0); elapsed < d; elapsed += 2 * time.Minute {
		c.clock.advance(2 * time.Minute)
		report(t, c, ctx, "PC-01", s)
	}
}

// CU-036-6: 3 h en trabajando y 5 h en esperando-ok sobre un issue.
func TestSessionFlow_CU0366(t *testing.T) {
	c := newClients(t)
	ctx := asUser(t, "u1")
	proj, err := c.issues.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "TK", Name: "Tekitla"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := proj.GetProject().GetId()
	if _, err := c.issues.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: "Página de métricas"}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	c.clock.advance(time.Second) // después del alta

	s := reported("s1", "TK", 1, byCwd, working)
	report(t, c, ctx, "PC-01", s)
	heartbeat(t, c, s, 3*time.Hour)

	s = reported("s1", "TK", 1, byCwd, waiting)
	s.WaitingReason = issuesv1.WaitingReason_WAITING_REASON_DECISION
	report(t, c, ctx, "PC-01", s)
	// El motivo se guarda y se lee (decisión 4). En esperando-ok sin token,
	// waiting_on queda NONE y la espera cuenta igual como espera a una
	// persona; con waiting_on NONE el motivo no se guarda.
	if got := onlySession(t, c, ctx, &issuesv1.ListAgentSessionsRequest{ProjectId: pid}); got.GetWaitingReason() != issuesv1.WaitingReason_WAITING_REASON_UNSPECIFIED {
		t.Errorf("reason without waiting_on: %v", got.GetWaitingReason())
	}
	s.WaitingOn = issuesv1.WaitingOn_WAITING_ON_PERSON
	report(t, c, ctx, "PC-01", s)
	if got := onlySession(t, c, ctx, &issuesv1.ListAgentSessionsRequest{ProjectId: pid}); got.GetWaitingReason() != issuesv1.WaitingReason_WAITING_REASON_DECISION {
		t.Errorf("reason: %v", got.GetWaitingReason())
	}
	heartbeat(t, c, s, 5*time.Hour)
	// El panel se cierra: la sesión termina.
	report(t, c, ctx, "PC-01")

	res, err := c.issues.GetIssueFlowEfficiency(ctx, &issuesv1.GetIssueFlowEfficiencyRequest{ProjectId: pid, Number: 1})
	if err != nil {
		t.Fatalf("GetIssueFlowEfficiency: %v", err)
	}
	e := res.GetEfficiency()
	if e.GetWorking().AsDuration() != 3*time.Hour || e.GetWaitingHuman().AsDuration() != 5*time.Hour || e.GetEfficiencyBp() != 3750 || e.GetIssues() != 1 {
		t.Fatalf("CU-036-6: %+v", e)
	}
	if e.EfficiencyBp == nil || res.GetRecordedSince() == nil || res.GetHumanQueue().GetPending() != 0 {
		t.Errorf("CU-036-6 extras: %+v", res)
	}
	byReason := map[issuesv1.WaitingReason]time.Duration{}
	for _, rt := range e.GetWaitingHumanByReason() {
		byReason[rt.GetReason()] = rt.GetDuration().AsDuration()
	}
	// Los dos primeros minutos fueron esperando-ok sin motivo.
	if byReason[issuesv1.WaitingReason_WAITING_REASON_DECISION] != 5*time.Hour || byReason[issuesv1.WaitingReason_WAITING_REASON_UNSPECIFIED] != 0 {
		t.Errorf("by reason: %v", byReason)
	}

	// Las métricas del proyecto traen lo mismo en el resumen, y la señal
	// dice que faltan semanas.
	fm, err := c.issues.GetFlowMetrics(ctx, &issuesv1.GetFlowMetricsRequest{ProjectId: pid})
	if err != nil {
		t.Fatalf("GetFlowMetrics: %v", err)
	}
	if se := fm.GetSummary().GetEfficiency(); se.GetEfficiencyBp() != 3750 || fm.GetSessionsRecordedSince() == nil {
		t.Errorf("summary efficiency: %+v, since %v", se, fm.GetSessionsRecordedSince())
	}
	sig := fm.GetHiringSignal()
	if sig.GetVerdict() != issuesv1.HiringVerdict_HIRING_VERDICT_INSUFFICIENT_DATA || sig.GetWeeksNeeded() != 8 || sig.GetWeeksNeededPreliminary() != 4 || sig.GetCurrent() != nil {
		t.Errorf("signal: %+v", sig)
	}
	// Las semanas anteriores a la historia no traen eficiencia.
	if w := fm.GetWeeks()[0]; w.GetEfficiency() != nil && w.GetEnd().AsTime().Before(fm.GetSessionsRecordedSince().AsTime()) {
		t.Errorf("week before history has efficiency: %+v", w)
	}
}

func TestSessionFlow_Errors(t *testing.T) {
	c := newClients(t)
	ctx := asUser(t, "u1")
	proj, err := c.issues.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "TK", Name: "Tekitla"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := proj.GetProject().GetId()
	if _, err := c.issues.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: "Uno"}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	// Sin historia: responde vacío, sin eficiencia.
	res, err := c.issues.GetIssueFlowEfficiency(ctx, &issuesv1.GetIssueFlowEfficiencyRequest{ProjectId: pid, Number: 1})
	if err != nil || res.GetEfficiency() != nil || res.GetRecordedSince() != nil {
		t.Errorf("no history: %+v, %v", res, err)
	}
	cases := map[string]struct {
		ctxUID string
		req    *issuesv1.GetIssueFlowEfficiencyRequest
		code   codes.Code
	}{
		"issue inexistente": {"u1", &issuesv1.GetIssueFlowEfficiencyRequest{ProjectId: pid, Number: 99}, codes.NotFound},
		"proyecto ajeno":    {"u2", &issuesv1.GetIssueFlowEfficiencyRequest{ProjectId: pid, Number: 1}, codes.NotFound},
		"número inválido":   {"u1", &issuesv1.GetIssueFlowEfficiencyRequest{ProjectId: pid, Number: 0}, codes.InvalidArgument},
		"sin identidad":     {"", &issuesv1.GetIssueFlowEfficiencyRequest{ProjectId: pid, Number: 1}, codes.Unauthenticated},
	}
	for name, tc := range cases {
		_, err := c.issues.GetIssueFlowEfficiency(asUser(t, tc.ctxUID), tc.req)
		if status.Code(err) != tc.code {
			t.Errorf("%s: got %v, want %v", name, status.Code(err), tc.code)
		}
	}
}

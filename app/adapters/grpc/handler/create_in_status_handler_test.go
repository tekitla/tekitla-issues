package handler_test

// Alta en una columna (issue 033, CU-033-3) de punta a punta: nace en el
// estado pedido, al final del orden, con el estado en el evento de alta, y
// las métricas lo cuentan desde el alta.

import (
	"testing"

	"google.golang.org/grpc/codes"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

func TestIssueHandler_CreateIssueEnUnEstado(t *testing.T) {
	client := newClient(t)
	ctx := asUser(t, "u1")

	created, err := client.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "TK", Name: "Tekitla"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := created.GetProject().GetId()
	sts, err := client.ListStatuses(ctx, &issuesv1.ListStatusesRequest{ProjectId: pid})
	if err != nil {
		t.Fatalf("ListStatuses: %v", err)
	}
	byState := map[issuesv1.IssueState]*issuesv1.Status{}
	for _, st := range sts.GetStatuses() {
		if byState[st.GetCategory()] == nil {
			byState[st.GetCategory()] = st
		}
	}
	doing, done, canceled := byState[issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS], byState[issuesv1.IssueState_ISSUE_STATE_DONE], byState[issuesv1.IssueState_ISSUE_STATE_CANCELED]

	// Sin status_id: como antes, en «por hacer».
	first, err := client.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: "por hacer"})
	if err != nil || first.GetIssue().GetState() != issuesv1.IssueState_ISSUE_STATE_TODO {
		t.Fatalf("CreateIssue sin status_id: %+v, %v", first.GetIssue(), err)
	}
	// En «en curso»: nace ahí, después del anterior en el orden.
	res, err := client.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: "en curso", StatusId: doing.GetId()})
	if err != nil {
		t.Fatalf("CreateIssue en curso: %v", err)
	}
	if is := res.GetIssue(); is.GetStatusId() != doing.GetId() || is.GetState() != issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS || is.GetRank() <= first.GetIssue().GetRank() {
		t.Errorf("issue en curso = %+v", is)
	}
	events, err := client.ListIssueEvents(ctx, &issuesv1.ListIssueEventsRequest{ProjectId: pid, Number: res.GetIssue().GetNumber()})
	if err != nil || len(events.GetEvents()) == 0 {
		t.Fatalf("ListIssueEvents: %v", err)
	}
	if c := events.GetEvents()[0].GetCreated(); c.GetStatusId() != doing.GetId() || c.GetStatusName() != doing.GetName() || c.GetState() != issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS {
		t.Errorf("evento de alta = %+v", events.GetEvents()[0])
	}
	// En «hecho»: cuenta como cerrado, sin lead time.
	if _, err := client.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: "ya hecho", StatusId: done.GetId()}); err != nil {
		t.Fatalf("CreateIssue hecho: %v", err)
	}
	m, err := client.GetFlowMetrics(ctx, &issuesv1.GetFlowMetricsRequest{ProjectId: pid})
	if err != nil {
		t.Fatalf("GetFlowMetrics: %v", err)
	}
	if s := m.GetSummary(); s.GetClosedValue() != 1 || s.GetLeadTime().GetCount() != 0 || s.GetStarted() != 1 || s.GetWip() != 1 || m.GetCoverage().GetDoneWithoutDates() != 0 {
		t.Errorf("métricas = %+v, cobertura %+v", s, m.GetCoverage())
	}

	// Descartado o de otro proyecto: INVALID_ARGUMENT con reason.
	other, err := client.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "OT", Name: "Otro"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	otherSts, _ := client.ListStatuses(ctx, &issuesv1.ListStatusesRequest{ProjectId: other.GetProject().GetId()})
	for name, c := range map[string]struct{ statusID, reason string }{
		"descartado":       {canceled.GetId(), "CANCELED_STATUS"},
		"de otro proyecto": {otherSts.GetStatuses()[0].GetId(), "STATUS_NOT_IN_PROJECT"},
		"no es un UUID":    {"hecho", "STATUS_NOT_IN_PROJECT"},
	} {
		_, err := client.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: "x", StatusId: c.statusID})
		wantCode(t, "CreateIssue "+name, err, codes.InvalidArgument)
		if got := errorReason(err); got != c.reason {
			t.Errorf("%s: reason %q, quiero %q", name, got, c.reason)
		}
	}
}

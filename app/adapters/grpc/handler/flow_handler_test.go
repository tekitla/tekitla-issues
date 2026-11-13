package handler_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// memFlow arma los FlowIssue con los issues y eventos del store en memoria.
type memFlow struct{ *memStore }

var _ port.FlowRepository = memFlow{}

func (s memFlow) ListFlowIssues(_ context.Context, projectID string, labelIDs []string) ([]model.FlowIssue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.FlowIssue
	for _, is := range s.issues {
		if is.ProjectID != projectID || (len(labelIDs) > 0 && !hasAny(is.LabelIDs, labelIDs)) {
			continue
		}
		fi := model.FlowIssue{Number: is.Number, Title: is.Title, StatusID: is.StatusID, State: is.State, CreatedAt: is.CreatedAt, LabelIDs: is.LabelIDs}
		for _, ev := range s.events {
			if ev.IssueID != is.ID {
				continue
			}
			fe := model.FlowEvent{At: ev.CreatedAt, Source: ev.Source, Created: ev.Kind == model.EventCreated}
			if ev.Status != nil {
				fe.Transition, fe.From, fe.To = true, ev.Status.FromCategory, ev.Status.ToCategory
			}
			fi.Events = append(fi.Events, fe)
		}
		out = append(out, fi)
	}
	return out, nil
}

func hasAny(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}

func errorReason(err error) string {
	for _, d := range status.Convert(err).Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

// GetFlowMetrics de punta a punta: identidad, pertenencia, validación con
// reason y un issue en vivo que pasa por en curso y termina.
func TestIssueHandler_GetFlowMetrics(t *testing.T) {
	client := newClient(t)
	ctx := asUser(t, "u1")

	created, err := client.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "TK", Name: "Tekitla"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := created.GetProject().GetId()
	feature, err := client.CreateLabel(ctx, &issuesv1.CreateLabelRequest{ProjectId: pid, Name: "feature", Value: true})
	if err != nil || !feature.GetLabel().GetValue() {
		t.Fatalf("CreateLabel de valor: %v %+v", err, feature)
	}
	for _, title := range []string{"con valor", "sin valor", "sigue en curso"} {
		if _, err := client.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: title}); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
	}
	if _, err := client.UpdateIssue(ctx, &issuesv1.UpdateIssueRequest{ProjectId: pid, Number: 1, Labels: &issuesv1.LabelIds{Ids: []string{feature.GetLabel().GetId()}}}); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	for _, mv := range []struct {
		n     int32
		state issuesv1.IssueState
	}{{1, issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS}, {1, issuesv1.IssueState_ISSUE_STATE_DONE}, {2, issuesv1.IssueState_ISSUE_STATE_DONE}, {3, issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS}} {
		if _, err := client.MoveIssue(ctx, &issuesv1.MoveIssueRequest{ProjectId: pid, Number: mv.n, State: mv.state}); err != nil {
			t.Fatalf("MoveIssue %d: %v", mv.n, err)
		}
	}

	// Un to futuro se toma como ahora (el reloj del servicio va un minuto
	// adelante, ver newClients).
	future := timestamppb.New(time.Now().Add(24 * time.Hour))
	res, err := client.GetFlowMetrics(ctx, &issuesv1.GetFlowMetricsRequest{ProjectId: pid, TimeZone: "UTC", To: future})
	if err != nil {
		t.Fatalf("GetFlowMetrics: %v", err)
	}
	if n := len(res.GetWeeks()); n < 12 || n > 13 {
		t.Errorf("semanas = %d, quiero 12 o 13 (12 semanas por defecto)", n)
	}
	s := res.GetSummary()
	if s.GetClosedValue() != 1 || s.GetClosedOther() != 1 || s.GetWip() != 1 || s.GetStarted() != 2 {
		t.Errorf("resumen = %+v", s)
	}
	if s.GetLeadTime().GetCount() != 2 || s.GetLeadTime().GetMedian() == nil || s.GetCycleTime().GetCount() != 1 {
		t.Errorf("lead y cycle time = %+v %+v", s.GetLeadTime(), s.GetCycleTime())
	}
	if s.GetSource().GetConfidence() != issuesv1.MetricConfidence_METRIC_CONFIDENCE_RECORDED || s.GetSource().GetLive() == 0 {
		t.Errorf("fuente = %+v, quiero registrada en vivo", s.GetSource())
	}
	if got := res.GetValueLabelIds(); len(got) != 1 || got[0] != feature.GetLabel().GetId() {
		t.Errorf("etiquetas de valor = %v", got)
	}
	if res.GetTo().AsTime().After(time.Now().Add(2 * time.Minute)) {
		t.Errorf("to = %v, quiero ahora (un to futuro se recorta)", res.GetTo().AsTime())
	}
	if res.GetTimeZone() != "UTC" || res.GetCoverage().GetIssues() != 3 || res.GetStaleTotal() != 0 {
		t.Errorf("respuesta = zona %q, cobertura %+v, estancados %d", res.GetTimeZone(), res.GetCoverage(), res.GetStaleTotal())
	}

	// Sin zona: la de México.
	res, err = client.GetFlowMetrics(ctx, &issuesv1.GetFlowMetricsRequest{ProjectId: pid})
	if err != nil || res.GetTimeZone() != "America/Mexico_City" {
		t.Errorf("zona por defecto = %q (%v)", res.GetTimeZone(), err)
	}

	// Filtro por etiqueta.
	res, err = client.GetFlowMetrics(ctx, &issuesv1.GetFlowMetricsRequest{ProjectId: pid, LabelIds: []string{feature.GetLabel().GetId()}})
	if err != nil || res.GetCoverage().GetIssues() != 1 || res.GetSummary().GetClosedValue() != 1 {
		t.Errorf("con filtro: %v %+v", err, res.GetCoverage())
	}

	// Errores.
	_, err = client.GetFlowMetrics(asUser(t, ""), &issuesv1.GetFlowMetricsRequest{ProjectId: pid})
	wantCode(t, "sin identidad", err, codes.Unauthenticated)
	_, err = client.GetFlowMetrics(asUser(t, "u2"), &issuesv1.GetFlowMetricsRequest{ProjectId: pid})
	wantCode(t, "proyecto ajeno", err, codes.NotFound)
	for _, c := range []struct {
		what   string
		req    *issuesv1.GetFlowMetricsRequest
		reason string
	}{
		{"zona desconocida", &issuesv1.GetFlowMetricsRequest{ProjectId: pid, TimeZone: "Marte/Olympus"}, "INVALID_TIME_ZONE"},
		{"from después de to", &issuesv1.GetFlowMetricsRequest{ProjectId: pid, From: timestamppb.New(ts("2026-06-10")), To: timestamppb.New(ts("2026-06-01"))}, "INVALID_PERIOD"},
		{"más de 104 semanas", &issuesv1.GetFlowMetricsRequest{ProjectId: pid, From: timestamppb.New(ts("2023-01-01")), To: timestamppb.New(ts("2026-06-01"))}, "PERIOD_TOO_LONG"},
		{"etiqueta ajena", &issuesv1.GetFlowMetricsRequest{ProjectId: pid, LabelIds: []string{"no-es-del-proyecto"}}, "UNKNOWN_LABEL"},
		{"stale_after_days fuera de rango", &issuesv1.GetFlowMetricsRequest{ProjectId: pid, StaleAfterDays: 400}, "INVALID_STALE_AFTER_DAYS"},
		{"stale_limit fuera de rango", &issuesv1.GetFlowMetricsRequest{ProjectId: pid, StaleLimit: 101}, "INVALID_STALE_LIMIT"},
		{"timestamp fuera de rango", &issuesv1.GetFlowMetricsRequest{ProjectId: pid, To: &timestamppb.Timestamp{Seconds: 1, Nanos: 2e9}}, "INVALID_PERIOD"},
	} {
		_, err := client.GetFlowMetrics(ctx, c.req)
		wantCode(t, c.what, err, codes.InvalidArgument)
		if got := errorReason(err); got != c.reason {
			t.Errorf("%s: reason %q, quiero %q", c.what, got, c.reason)
		}
	}
}

func ts(day string) time.Time {
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		panic(err)
	}
	return t
}

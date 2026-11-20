package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

const labelBug = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

var flowNow = time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)

func flowLabels() *mockLabelRepo {
	return &mockLabelRepo{listFn: func(context.Context, string) ([]*model.Label, error) {
		return []*model.Label{{ID: labelFront, Name: "front"}, {ID: labelBug, Name: "bug", CountsAsValue: true}}, nil
	}}
}

func newFlowService(flow *mockFlowRepo) *application.FlowService {
	return application.NewFlowService(personalWorkspaces(), ownedProjects(), flowLabels(), flow).
		WithClock(func() time.Time { return flowNow })
}

func TestGetFlowMetrics_Defaults(t *testing.T) {
	var gotLabels []string
	flow := &mockFlowRepo{listFn: func(_ context.Context, projectID string, labelIDs []string) ([]model.FlowIssue, error) {
		gotLabels = labelIDs
		return []model.FlowIssue{{
			Number: 1, State: model.IssueStateDone, CreatedAt: flowNow.AddDate(0, 0, -10), LabelIDs: []string{labelBug},
			Events: []model.FlowEvent{{At: flowNow.AddDate(0, 0, -2), Transition: true, From: model.IssueStateTodo, To: model.IssueStateDone}},
		}}, nil
	}}
	res, err := newFlowService(flow).GetFlowMetrics(context.Background(), owner, application.FlowMetricsInput{ProjectID: validID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.To.Equal(flowNow) || !res.From.Equal(flowNow.AddDate(0, 0, -84)) || res.TimeZone != "America/Mexico_City" {
		t.Errorf("período = %v..%v %s, quiero 12 semanas hasta ahora en México", res.From, res.To, res.TimeZone)
	}
	if len(res.ValueLabelIDs) != 1 || res.ValueLabelIDs[0] != labelBug {
		t.Errorf("etiquetas de valor = %v", res.ValueLabelIDs)
	}
	if gotLabels != nil {
		t.Errorf("sin filtro el repo recibió %v", gotLabels)
	}
	if res.Metrics.Summary.ClosedValue != 1 || res.Metrics.Summary.LeadTime.Median != 8*24*time.Hour {
		t.Errorf("resumen = %+v", res.Metrics.Summary)
	}
}

func TestGetFlowMetrics_LabelFilter(t *testing.T) {
	var gotLabels []string
	flow := &mockFlowRepo{listFn: func(_ context.Context, _ string, labelIDs []string) ([]model.FlowIssue, error) {
		gotLabels = labelIDs
		return nil, nil
	}}
	in := application.FlowMetricsInput{ProjectID: validID, LabelIDs: []string{labelFront, labelFront}}
	if _, err := newFlowService(flow).GetFlowMetrics(context.Background(), owner, in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotLabels) != 1 || gotLabels[0] != labelFront {
		t.Errorf("filtro = %v, quiero la etiqueta sin repetir", gotLabels)
	}
}

func TestGetFlowMetrics_Validation(t *testing.T) {
	svc := newFlowService(&mockFlowRepo{})
	cases := map[string]struct {
		in     application.FlowMetricsInput
		reason string
	}{
		"zona desconocida":   {application.FlowMetricsInput{TimeZone: "Marte/Olympus"}, "INVALID_TIME_ZONE"},
		"zona Local":         {application.FlowMetricsInput{TimeZone: "Local"}, "INVALID_TIME_ZONE"},
		"from igual a to":    {application.FlowMetricsInput{From: flowNow, To: flowNow}, "INVALID_PERIOD"},
		"from después de to": {application.FlowMetricsInput{From: flowNow, To: flowNow.AddDate(0, 0, -1)}, "INVALID_PERIOD"},
		"más de 104 semanas": {application.FlowMetricsInput{From: flowNow.AddDate(-2, 0, -1)}, "PERIOD_TOO_LONG"},
		"etiqueta ajena":     {application.FlowMetricsInput{LabelIDs: []string{"otra"}}, ""},
		"estancado 0 días":   {application.FlowMetricsInput{StaleAfterDays: -1}, ""},
		"estancado 366 días": {application.FlowMetricsInput{StaleAfterDays: 366}, ""},
		"tope de estancados": {application.FlowMetricsInput{StaleLimit: 101}, ""},
		"tope negativo":      {application.FlowMetricsInput{StaleLimit: -1}, ""},
		"proyecto sin UUID":  {application.FlowMetricsInput{ProjectID: "x"}, ""},
	}
	for name, c := range cases {
		if c.in.ProjectID == "" {
			c.in.ProjectID = validID
		}
		_, err := svc.GetFlowMetrics(context.Background(), owner, c.in)
		if !domainerrors.IsValidation(err) {
			t.Errorf("%s: quiero ValidationError, got %v", name, err)
			continue
		}
		if c.reason != "" {
			var de *domainerrors.DomainError
			if !errors.As(err, &de) || de.Reason != c.reason {
				t.Errorf("%s: reason %v, quiero %s", name, err, c.reason)
			}
		}
	}
	// Los límites valen.
	ok := application.FlowMetricsInput{ProjectID: validID, From: flowNow.AddDate(0, 0, -7*104), StaleAfterDays: 365, StaleLimit: 100}
	if _, err := svc.GetFlowMetrics(context.Background(), owner, ok); err != nil {
		t.Errorf("en los límites: %v", err)
	}
}

func TestGetFlowMetrics_Errors(t *testing.T) {
	if _, err := newFlowService(&mockFlowRepo{}).GetFlowMetrics(context.Background(), "", application.FlowMetricsInput{ProjectID: validID}); err == nil {
		t.Error("sin usuario: quiero error")
	}
	boom := domainerrors.InternalError("boom", nil)
	flow := &mockFlowRepo{listFn: func(context.Context, string, []string) ([]model.FlowIssue, error) { return nil, boom }}
	if _, err := newFlowService(flow).GetFlowMetrics(context.Background(), owner, application.FlowMetricsInput{ProjectID: validID}); !domainerrors.IsInternal(err) {
		t.Errorf("error del repo: got %v", err)
	}
	labels := &mockLabelRepo{listFn: func(context.Context, string) ([]*model.Label, error) { return nil, boom }}
	svc := application.NewFlowService(personalWorkspaces(), ownedProjects(), labels, &mockFlowRepo{})
	if _, err := svc.GetFlowMetrics(context.Background(), owner, application.FlowMetricsInput{ProjectID: validID}); !domainerrors.IsInternal(err) {
		t.Errorf("error de etiquetas: got %v", err)
	}
}

// Un to futuro se toma como ahora.
func TestGetFlowMetrics_FutureTo(t *testing.T) {
	in := application.FlowMetricsInput{ProjectID: validID, To: flowNow.AddDate(0, 0, 60)}
	res, err := newFlowService(&mockFlowRepo{}).GetFlowMetrics(context.Background(), owner, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.To.Equal(flowNow) {
		t.Errorf("to = %v, quiero ahora (%v)", res.To, flowNow)
	}
}

// Issue 036, parte 3: GetIssueFlowEfficiency y la parte de sesiones de
// GetFlowMetrics (datos inventados).

func issueFlowRepo(tracks []model.SessionTrack, tracksErr error) *mockFlowRepo {
	return &mockFlowRepo{
		listFn: func(context.Context, string, []string) ([]model.FlowIssue, error) {
			return []model.FlowIssue{{Number: 7, State: model.IssueStateInProgress, CreatedAt: flowNow.AddDate(0, 0, -3)}}, nil
		},
		tracksFn: func(context.Context, string, int32, time.Time) ([]model.SessionTrack, error) {
			return tracks, tracksErr
		},
	}
}

func TestGetIssueFlowEfficiency(t *testing.T) {
	ctx := context.Background()
	at := func(h int) time.Time { return flowNow.Add(time.Duration(h-24) * time.Hour) }
	tracks := []model.SessionTrack{{SessionID: "s1", LastSeenAt: flowNow, Transitions: []model.SessionTransition{
		{SessionID: "s1", At: at(10), Status: model.SessionWorking, WaitingOn: model.WaitingNone, ProjectID: validID, IssueNumber: 7},
		{SessionID: "s1", At: at(13), Status: model.SessionWaitingOK, WaitingOn: model.WaitingNone, ProjectID: validID, IssueNumber: 7},
		{SessionID: "s1", At: at(18), Status: model.SessionWorking, WaitingOn: model.WaitingNone, ProjectID: validID, IssueNumber: 7},
	}}}
	svc := newFlowService(issueFlowRepo(tracks, nil))

	// CU-036-6 hasta el momento de la respuesta: 3 h trabajando y 5 h
	// esperando; la respuesta justo en to no deja la espera pendiente.
	res, err := svc.GetIssueFlowEfficiency(ctx, owner, application.IssueFlowInput{ProjectID: validID, Number: 7, To: at(18)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bp, ok := res.Efficiency.EfficiencyBP()
	if !res.Has || !ok || bp != 3750 || res.Efficiency.WaitingHuman != 5*time.Hour || res.Queue.Pending != 0 {
		t.Errorf("CU-036-6: %+v (bp %d)", res, bp)
	}

	// Un to anterior al alta, sin from: vacío, no un error sobre from.
	res, err = svc.GetIssueFlowEfficiency(ctx, owner, application.IssueFlowInput{ProjectID: validID, Number: 7, To: flowNow.AddDate(0, 0, -10)})
	if err != nil || res.Has {
		t.Errorf("to before created: %+v, %v", res, err)
	}

	for name, in := range map[string]application.IssueFlowInput{
		"número inválido":    {ProjectID: validID, Number: 0},
		"from después de to": {ProjectID: validID, Number: 7, From: flowNow.Add(-time.Hour), To: flowNow.Add(-2 * time.Hour)},
	} {
		if _, err := svc.GetIssueFlowEfficiency(ctx, owner, in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: want validation, got %v", name, err)
		}
	}
	if _, err := svc.GetIssueFlowEfficiency(ctx, owner, application.IssueFlowInput{ProjectID: validID, Number: 99}); !domainerrors.IsNotFound(err) {
		t.Errorf("unknown issue: want not found, got %v", err)
	}
}

func TestFlowSessions_RepoErrorsPropagate(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	svc := newFlowService(issueFlowRepo(nil, boom))
	if _, err := svc.GetIssueFlowEfficiency(ctx, owner, application.IssueFlowInput{ProjectID: validID, Number: 7}); !errors.Is(err, boom) {
		t.Errorf("GetIssueFlowEfficiency: want boom, got %v", err)
	}
	if _, err := svc.GetFlowMetrics(ctx, owner, application.FlowMetricsInput{ProjectID: validID}); !errors.Is(err, boom) {
		t.Errorf("GetFlowMetrics: want boom, got %v", err)
	}
}

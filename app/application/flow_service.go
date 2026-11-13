package application

import (
	"context"
	"strings"
	"time"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// FlowService calcula las métricas de flujo de un proyecto (issue 036).
type FlowService struct {
	guard  projectGuard
	labels port.LabelRepository
	flow   port.FlowRepository
	now    func() time.Time
}

// NewFlowService construye el servicio.
func NewFlowService(workspaceRepo port.WorkspaceRepository, projects port.ProjectRepository, labels port.LabelRepository, flow port.FlowRepository) *FlowService {
	return &FlowService{
		guard:  projectGuard{workspaces: workspaces{repo: workspaceRepo}, projects: projects},
		labels: labels,
		flow:   flow,
		now:    utcNow,
	}
}

// WithClock fija el reloj (tests).
func (s *FlowService) WithClock(now func() time.Time) *FlowService {
	s.now = now
	return s
}

// FlowMetricsInput son los parámetros de GetFlowMetrics. Cero = por defecto.
type FlowMetricsInput struct {
	ProjectID      string
	From, To       time.Time
	TimeZone       string
	LabelIDs       []string
	StaleAfterDays int32
	StaleLimit     int32
}

// FlowMetricsResult es el cálculo con el período efectivo.
type FlowMetricsResult struct {
	From, To      time.Time
	TimeZone      string
	ValueLabelIDs []string
	Metrics       model.FlowMetrics
}

// GetFlowMetrics valida el período y las opciones, lee el proyecto y
// calcula sus métricas.
func (s *FlowService) GetFlowMetrics(ctx context.Context, ownerUID string, in FlowMetricsInput) (*FlowMetricsResult, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, in.ProjectID); err != nil {
		return nil, err
	}
	q, tz, err := s.flowQuery(in)
	if err != nil {
		return nil, err
	}
	labels, err := s.labels.List(ctx, in.ProjectID)
	if err != nil {
		return nil, err
	}
	filter, err := projectLabelIDs(labels, in.LabelIDs)
	if err != nil {
		return nil, err
	}
	q.ValueLabelIDs = []string{}
	for _, l := range labels {
		if l.Value {
			q.ValueLabelIDs = append(q.ValueLabelIDs, l.ID)
		}
	}
	issues, err := s.flow.ListFlowIssues(ctx, in.ProjectID, filter)
	if err != nil {
		return nil, err
	}
	return &FlowMetricsResult{
		From: q.From, To: q.To, TimeZone: tz, ValueLabelIDs: q.ValueLabelIDs,
		Metrics: model.ComputeFlowMetrics(issues, q),
	}, nil
}

func (s *FlowService) flowQuery(in FlowMetricsInput) (model.FlowQuery, string, error) {
	var q model.FlowQuery
	tz := strings.TrimSpace(in.TimeZone)
	if tz == "" {
		tz = model.FlowDefaultTimeZone
	}
	loc, err := time.LoadLocation(tz)
	// LoadLocation acepta "Local" y "": solo zonas IANA con nombre.
	if err != nil || tz == "Local" {
		return q, "", domainerrors.ValidationError("time_zone must be an IANA time zone, e.g. America/Mexico_City", map[string]any{"field": "time_zone", "value": in.TimeZone}).WithReason("INVALID_TIME_ZONE")
	}
	q.Location = loc

	q.To = in.To
	if q.To.IsZero() {
		q.To = s.now()
	}
	q.From = in.From
	if q.From.IsZero() {
		q.From = q.To.AddDate(0, 0, -7*model.FlowDefaultWeeks)
	}
	if !q.From.Before(q.To) {
		return q, "", domainerrors.ValidationError("from must be before to", map[string]any{"field": "from"}).WithReason("INVALID_PERIOD")
	}
	if q.To.Sub(q.From) > time.Duration(model.FlowMaxWeeks)*7*24*time.Hour {
		return q, "", domainerrors.ValidationError("the period can be at most 104 weeks", map[string]any{"field": "from", "max_weeks": model.FlowMaxWeeks}).WithReason("PERIOD_TOO_LONG")
	}

	switch d := in.StaleAfterDays; {
	case d == 0:
		q.StaleDays = model.FlowDefaultStaleDays
	case d < 1 || d > model.FlowMaxStaleDays:
		return q, "", domainerrors.ValidationError("stale_after_days must be between 1 and 365", map[string]any{"field": "stale_after_days", "value": d})
	default:
		q.StaleDays = int(d)
	}
	switch n := in.StaleLimit; {
	case n == 0:
		q.StaleMax = model.FlowDefaultStaleMax
	case n < 1 || n > model.FlowMaxStaleMax:
		return q, "", domainerrors.ValidationError("stale_limit must be between 1 and 100", map[string]any{"field": "stale_limit", "value": n})
	default:
		q.StaleMax = int(n)
	}
	return q, tz, nil
}

// projectLabelIDs valida que los ids sean etiquetas del proyecto y los
// devuelve sin repetidos.
func projectLabelIDs(labels []*model.Label, ids []string) ([]string, error) {
	known := make(map[string]bool, len(labels))
	for _, l := range labels {
		known[l.ID] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if !known[id] {
			return nil, domainerrors.ValidationError("label_ids must be labels of the project", map[string]any{"field": "label_ids", "value": id})
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

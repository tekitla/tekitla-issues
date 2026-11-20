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
	guard      projectGuard
	labels     port.LabelRepository
	flow       port.FlowRepository
	stallAfter time.Duration
	now        func() time.Time
}

// NewFlowService construye el servicio.
func NewFlowService(workspaceRepo port.WorkspaceRepository, projects port.ProjectRepository, labels port.LabelRepository, flow port.FlowRepository) *FlowService {
	return &FlowService{
		guard:      projectGuard{workspaces: workspaces{repo: workspaceRepo}, projects: projects},
		labels:     labels,
		flow:       flow,
		stallAfter: model.DefaultSessionStallAfter,
		now:        utcNow,
	}
}

// WithStallAfter fija el umbral sin reportes desde el que una sesión deja de
// contar (SESSION_STALL_AFTER, el mismo de «colgado»). <= 0 no cambia nada.
func (s *FlowService) WithStallAfter(d time.Duration) *FlowService {
	if d > 0 {
		s.stallAfter = d
	}
	return s
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
	// Sessions: eficiencia y cola humana por semana (issue 036, parte 3).
	Sessions model.SessionFlow
	// Signal: la señal de contratación de las 8 semanas antes de To.
	Signal model.HiringSignal
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
		if l.CountsAsValue {
			q.ValueLabelIDs = append(q.ValueLabelIDs, l.ID)
		}
	}
	issues, err := s.flow.ListFlowIssues(ctx, in.ProjectID, filter)
	if err != nil {
		return nil, err
	}
	res := &FlowMetricsResult{
		From: q.From, To: q.To, TimeZone: tz, ValueLabelIDs: q.ValueLabelIDs,
		Metrics: model.ComputeFlowMetrics(issues, q),
	}

	// Parte 3: las sesiones de los mismos issues (con el filtro de
	// etiquetas). La historia empieza en el proyecto, no en el filtro.
	tracks, err := s.flow.ListSessionTracks(ctx, in.ProjectID, 0, q.To)
	if err != nil {
		return nil, err
	}
	known := make(map[int32]bool, len(issues))
	for _, is := range issues {
		known[is.Number] = true
	}
	keep := func(k model.IssueKey) bool { return k.ProjectID == in.ProjectID && known[k.Number] }
	now := s.now()
	segs := model.IssueSegments(tracks, keep, now, s.stallAfter)
	since, hasSince := model.RecordedSince(tracks, in.ProjectID, 0)
	res.Sessions = model.ComputeSessionWeeks(segs, model.FlowWeekBounds(q.From, q.To, q.Location), since, hasSince, now)

	sigFrom, sigTo := model.SignalBounds(q.To, q.Location)
	sq := q
	sq.From, sq.To, sq.StaleMax = sigFrom, sigTo, 0
	res.Signal = model.ComputeHiringSignal(model.ComputeFlowMetrics(issues, sq).Weeks, segs, since, hasSince, now)
	return res, nil
}

// IssueFlowInput son los parámetros de GetIssueFlowEfficiency. Cero = por
// defecto.
type IssueFlowInput struct {
	ProjectID string
	Number    int32
	From, To  time.Time
}

// IssueFlowResult es la eficiencia de un issue en el período. Has: el issue
// tiene historia de sesiones antes de To.
type IssueFlowResult struct {
	Has           bool
	Efficiency    model.FlowEfficiency
	Queue         model.HumanQueue
	RecordedSince time.Time
	HasSince      bool
}

// GetIssueFlowEfficiency calcula dónde se fue el tiempo de un issue según
// sus sesiones (CU-036-6).
func (s *FlowService) GetIssueFlowEfficiency(ctx context.Context, ownerUID string, in IssueFlowInput) (*IssueFlowResult, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, in.ProjectID); err != nil {
		return nil, err
	}
	if in.Number <= 0 {
		return nil, domainerrors.ValidationError("number must be positive", map[string]any{"field": "number"})
	}
	issues, err := s.flow.ListFlowIssues(ctx, in.ProjectID, nil)
	if err != nil {
		return nil, err
	}
	var issue *model.FlowIssue
	for i := range issues {
		if issues[i].Number == in.Number {
			issue = &issues[i]
		}
	}
	if issue == nil {
		return nil, domainerrors.NotFoundError("issue not found", map[string]any{"project_id": in.ProjectID, "number": in.Number})
	}
	now := s.now()
	to := in.To
	if to.IsZero() || to.After(now) {
		to = now
	}
	from := in.From
	if from.IsZero() {
		from = issue.CreatedAt
	}
	if !from.Before(to) {
		return nil, domainerrors.ValidationError("from must be before to", map[string]any{"field": "from"}).WithReason("INVALID_PERIOD")
	}
	tracks, err := s.flow.ListSessionTracks(ctx, in.ProjectID, in.Number, to)
	if err != nil {
		return nil, err
	}
	key := model.IssueKey{ProjectID: in.ProjectID, Number: in.Number}
	segs := model.IssueSegments(tracks, func(k model.IssueKey) bool { return k == key }, now, s.stallAfter)
	res := &IssueFlowResult{}
	res.RecordedSince, res.HasSince = model.RecordedSince(tracks, in.ProjectID, in.Number)
	if res.HasSince && res.RecordedSince.Before(to) {
		res.Has = true
		res.Efficiency = model.EfficiencyIn(segs, from, to)
		res.Queue = model.HumanQueueIn(segs, from, to, now)
	}
	return res, nil
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

	// Un to futuro se toma como ahora: el WIP y los estancados de un instante
	// que no pasó no existen.
	now := s.now()
	q.To = in.To
	if q.To.IsZero() || q.To.After(now) {
		q.To = now
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
		return q, "", domainerrors.ValidationError("stale_after_days must be between 1 and 365", map[string]any{"field": "stale_after_days", "value": d}).WithReason("INVALID_STALE_AFTER_DAYS")
	default:
		q.StaleDays = int(d)
	}
	switch n := in.StaleLimit; {
	case n == 0:
		q.StaleMax = model.FlowDefaultStaleMax
	case n < 1 || n > model.FlowMaxStaleMax:
		return q, "", domainerrors.ValidationError("stale_limit must be between 1 and 100", map[string]any{"field": "stale_limit", "value": n}).WithReason("INVALID_STALE_LIMIT")
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
			return nil, domainerrors.ValidationError("label_ids must be labels of the project", map[string]any{"field": "label_ids", "value": id}).WithReason("UNKNOWN_LABEL")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

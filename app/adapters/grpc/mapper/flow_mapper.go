package mapper

import (
	"google.golang.org/protobuf/types/known/durationpb"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/application"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// FlowMetricsInputFromProto arma la entrada de GetFlowMetrics. Un from o to
// ausente queda en cero (el service pone el valor por defecto).
func FlowMetricsInputFromProto(req *issuesv1.GetFlowMetricsRequest) application.FlowMetricsInput {
	in := application.FlowMetricsInput{
		ProjectID:      req.GetProjectId(),
		TimeZone:       req.GetTimeZone(),
		LabelIDs:       req.GetLabelIds(),
		StaleAfterDays: req.GetStaleAfterDays(),
		StaleLimit:     req.GetStaleLimit(),
	}
	if req.From != nil {
		in.From = req.GetFrom().AsTime()
	}
	if req.To != nil {
		in.To = req.GetTo().AsTime()
	}
	return in
}

// FlowMetricsToProto convierte el resultado de GetFlowMetrics.
func FlowMetricsToProto(r *application.FlowMetricsResult) *issuesv1.GetFlowMetricsResponse {
	m := r.Metrics
	out := &issuesv1.GetFlowMetricsResponse{
		From:          TimestampToProto(r.From),
		To:            TimestampToProto(r.To),
		TimeZone:      r.TimeZone,
		Weeks:         make([]*issuesv1.FlowWeek, len(m.Weeks)),
		Stale:         make([]*issuesv1.StaleIssue, len(m.Stale)),
		StaleTotal:    int32(m.StaleTotal),
		ValueLabelIds: r.ValueLabelIDs,
		Coverage:      &issuesv1.FlowCoverage{Issues: int32(m.Issues), DoneWithoutDates: int32(m.DoneWithoutDates)},
		Summary: &issuesv1.FlowSummary{
			LeadTime:    durationStatsToProto(m.Summary.LeadTime),
			CycleTime:   durationStatsToProto(m.Summary.CycleTime),
			ClosedValue: int32(m.Summary.ClosedValue),
			ClosedOther: int32(m.Summary.ClosedOther),
			Canceled:    int32(m.Summary.Canceled),
			Started:     int32(m.Summary.Started),
			Wip:         int32(m.Summary.WIP),
			Source:      metricSourceToProto(m.Summary.Source),
		},
	}
	for i, w := range m.Weeks {
		out.Weeks[i] = &issuesv1.FlowWeek{
			Start:       TimestampToProto(w.Start),
			End:         TimestampToProto(w.End),
			LeadTime:    durationStatsToProto(w.LeadTime),
			CycleTime:   durationStatsToProto(w.CycleTime),
			ClosedValue: int32(w.ClosedValue),
			ClosedOther: int32(w.ClosedOther),
			Canceled:    int32(w.Canceled),
			Started:     int32(w.Started),
			Wip:         int32(w.WIP),
			Source:      metricSourceToProto(w.Source),
		}
	}
	for i, s := range m.Stale {
		out.Stale[i] = &issuesv1.StaleIssue{
			Number:          s.Number,
			Title:           s.Title,
			StatusId:        s.StatusID,
			InProgressSince: TimestampToProto(s.InProgressSince),
			LastActivityAt:  TimestampToProto(s.LastActivityAt),
			Confidence:      metricConfidenceToProto(s.Confidence),
		}
	}
	return out
}

func durationStatsToProto(s model.DurationStats) *issuesv1.DurationStats {
	out := &issuesv1.DurationStats{Count: int32(s.Count)}
	if s.Count > 0 {
		out.Median = durationpb.New(s.Median)
		out.P85 = durationpb.New(s.P85)
	}
	return out
}

func metricSourceToProto(s model.MetricSource) *issuesv1.MetricSource {
	return &issuesv1.MetricSource{
		Confidence:    metricConfidenceToProto(s.Confidence()),
		Live:          int32(s.Live),
		ImportHistory: int32(s.ImportHistory),
		ImportFile:    int32(s.ImportFile),
		ImportSync:    int32(s.ImportSync),
	}
}

func metricConfidenceToProto(c model.MetricConfidence) issuesv1.MetricConfidence {
	switch c {
	case model.MetricConfidenceNoData:
		return issuesv1.MetricConfidence_METRIC_CONFIDENCE_NO_DATA
	case model.MetricConfidenceRecorded:
		return issuesv1.MetricConfidence_METRIC_CONFIDENCE_RECORDED
	case model.MetricConfidenceApproximate:
		return issuesv1.MetricConfidence_METRIC_CONFIDENCE_APPROXIMATE
	}
	return issuesv1.MetricConfidence_METRIC_CONFIDENCE_UNSPECIFIED
}

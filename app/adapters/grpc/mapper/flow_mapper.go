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
	sf := r.Sessions
	if sf.HasSince {
		out.SessionsRecordedSince = TimestampToProto(sf.Since)
	}
	if sf.HasSummary {
		out.Summary.Efficiency = FlowEfficiencyToProto(sf.Summary)
		out.Summary.HumanQueue = HumanQueueToProto(sf.SummaryQueue)
	}
	out.HiringSignal = hiringSignalToProto(r.Signal)
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
		if i < len(sf.Weeks) && sf.Weeks[i].Has {
			out.Weeks[i].Efficiency = FlowEfficiencyToProto(sf.Weeks[i].Efficiency)
			out.Weeks[i].HumanQueue = HumanQueueToProto(sf.Weeks[i].Queue)
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

// FlowEfficiencyToProto convierte la eficiencia (issue 036, CU-036-6).
func FlowEfficiencyToProto(e model.FlowEfficiency) *issuesv1.FlowEfficiency {
	out := &issuesv1.FlowEfficiency{
		Working:      durationpb.New(e.Working),
		WaitingHuman: durationpb.New(e.WaitingHuman),
		Blocked:      durationpb.New(e.Blocked),
		InReview:     durationpb.New(e.InReview),
		Idle:         durationpb.New(e.Idle),
		Issues:       int32(e.Issues),
	}
	if bp, ok := e.EfficiencyBP(); ok {
		v := int32(bp)
		out.EfficiencyBp = &v
	}
	for _, rt := range e.ReasonTimes() {
		out.WaitingHumanByReason = append(out.WaitingHumanByReason, &issuesv1.ReasonTime{
			Reason: WaitingReasonToProto(rt.Reason), Duration: durationpb.New(rt.Duration),
		})
	}
	return out
}

// HumanQueueToProto convierte la cola humana.
func HumanQueueToProto(q model.HumanQueue) *issuesv1.HumanQueue {
	out := &issuesv1.HumanQueue{Pending: int32(q.Pending), ResponseTime: durationStatsToProto(q.Response)}
	if q.Pending > 0 {
		out.OldestPending = durationpb.New(q.OldestPending)
	}
	return out
}

var hiringVerdictTo = map[model.HiringVerdict]issuesv1.HiringVerdict{
	model.HiringVerdictInsufficientData: issuesv1.HiringVerdict_HIRING_VERDICT_INSUFFICIENT_DATA,
	model.HiringVerdictNoSignal:         issuesv1.HiringVerdict_HIRING_VERDICT_NO_SIGNAL,
	model.HiringVerdictNeedsPeople:      issuesv1.HiringVerdict_HIRING_VERDICT_NEEDS_PEOPLE,
	model.HiringVerdictReduceWIP:        issuesv1.HiringVerdict_HIRING_VERDICT_REDUCE_WIP,
}

func hiringSignalToProto(s model.HiringSignal) *issuesv1.HiringSignal {
	out := &issuesv1.HiringSignal{
		Verdict:                hiringVerdictTo[s.Verdict],
		LiveWeeks:              int32(s.LiveWeeks),
		WeeksNeeded:            model.SignalWeeksNeeded,
		WeeksNeededPreliminary: model.SignalWeeksNeededPreliminary,
		Preliminary:            s.Preliminary,
		WipHigh:                s.WIPHigh,
		Current:                signalWindowToProto(s.Current),
		Previous:               signalWindowToProto(s.Previous),
	}
	for _, p := range s.Profiles {
		out.Profiles = append(out.Profiles, &issuesv1.ProfileShare{Reason: WaitingReasonToProto(p.Reason), ShareBp: int32(p.ShareBP)})
	}
	return out
}

func signalWindowToProto(w *model.SignalWindow) *issuesv1.SignalWindow {
	if w == nil {
		return nil
	}
	return &issuesv1.SignalWindow{
		Start:         TimestampToProto(w.Start),
		End:           TimestampToProto(w.End),
		Efficiency:    FlowEfficiencyToProto(w.Efficiency),
		HumanResponse: durationStatsToProto(w.HumanResponse),
		WipMedian:     int32(w.WIPMedian),
		Closed:        int32(w.Closed),
	}
}

// IssueFlowInputFromProto arma la entrada de GetIssueFlowEfficiency.
func IssueFlowInputFromProto(req *issuesv1.GetIssueFlowEfficiencyRequest) application.IssueFlowInput {
	in := application.IssueFlowInput{ProjectID: req.GetProjectId(), Number: req.GetNumber()}
	if req.From != nil {
		in.From = req.GetFrom().AsTime()
	}
	if req.To != nil {
		in.To = req.GetTo().AsTime()
	}
	return in
}

// IssueFlowToProto convierte la eficiencia de un issue.
func IssueFlowToProto(r *application.IssueFlowResult) *issuesv1.GetIssueFlowEfficiencyResponse {
	out := &issuesv1.GetIssueFlowEfficiencyResponse{}
	if r.HasSince {
		out.RecordedSince = TimestampToProto(r.RecordedSince)
	}
	if r.Has {
		out.Efficiency = FlowEfficiencyToProto(r.Efficiency)
		out.HumanQueue = HumanQueueToProto(r.Queue)
	}
	return out
}

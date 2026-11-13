package model

import (
	"math"
	"slices"
	"sort"
	"time"
)

// Métricas de flujo de un proyecto (issue 036, parte 2). El cálculo es puro:
// recibe los issues con sus eventos y el período, y no lee la base.

// Límites y valores por defecto de GetFlowMetrics.
const (
	FlowDefaultWeeks     = 12
	FlowMaxWeeks         = 104
	FlowDefaultTimeZone  = "America/Mexico_City"
	FlowDefaultStaleDays = 30
	FlowMaxStaleDays     = 365
	FlowDefaultStaleMax  = 20
	FlowMaxStaleMax      = 100
)

// FlowEvent es un evento del historial de un issue, reducido a lo que usan
// las métricas.
type FlowEvent struct {
	At     time.Time
	Source EventSource
	// Transition: cambio de categoría (status_changed, discarded, restored).
	// From y To solo valen en ese caso.
	Transition bool
	From, To   IssueState
	// Created: el evento «creado» (su fuente es la del alta).
	Created bool
}

// FlowIssue es un issue con lo que usan las métricas. Events va ordenado
// como el historial: (created_at, seq).
type FlowIssue struct {
	Number    int32
	Title     string
	StatusID  string
	State     IssueState
	CreatedAt time.Time
	LabelIDs  []string
	Events    []FlowEvent
}

// FlowQuery es el período y las opciones ya validadas.
type FlowQuery struct {
	From, To  time.Time
	Location  *time.Location
	StaleDays int
	StaleMax  int
	// ValueLabelIDs: etiquetas de valor del proyecto. Vacío: todo es valor.
	ValueLabelIDs []string
}

// MetricConfidence es qué tan confiable es un número.
type MetricConfidence int

// Confianzas (mismo orden que el enum del proto).
const (
	MetricConfidenceUnspecified MetricConfidence = iota
	MetricConfidenceNoData
	MetricConfidenceRecorded
	MetricConfidenceApproximate
)

// MetricSource cuenta las transiciones de cada fuente que sostienen un
// número.
type MetricSource struct {
	Live, ImportHistory, ImportFile, ImportSync int
}

func (s *MetricSource) add(src EventSource) {
	switch src.OrLive() {
	case EventSourceImportHistory:
		s.ImportHistory++
	case EventSourceImportFile:
		s.ImportFile++
	case EventSourceImportSync:
		s.ImportSync++
	default:
		s.Live++
	}
}

func (s *MetricSource) merge(o MetricSource) {
	s.Live += o.Live
	s.ImportHistory += o.ImportHistory
	s.ImportFile += o.ImportFile
	s.ImportSync += o.ImportSync
}

// Confidence: sin transiciones, NoData; todas en vivo, Recorded; alguna
// importada, Approximate.
func (s MetricSource) Confidence() MetricConfidence {
	switch {
	case s.Live+s.ImportHistory+s.ImportFile+s.ImportSync == 0:
		return MetricConfidenceNoData
	case s.ImportHistory+s.ImportFile+s.ImportSync == 0:
		return MetricConfidenceRecorded
	}
	return MetricConfidenceApproximate
}

// confidenceOf es la confianza de un solo dato.
func confidenceOf(src EventSource) MetricConfidence {
	var s MetricSource
	s.add(src)
	return s.Confidence()
}

// DurationStats es la mediana y el percentil 85 de una duración (rango más
// cercano). Median y P85 valen solo si Count > 0.
type DurationStats struct {
	Count       int
	Median, P85 time.Duration
}

func durationStats(samples []time.Duration) DurationStats {
	if len(samples) == 0 {
		return DurationStats{}
	}
	s := slices.Clone(samples)
	slices.Sort(s)
	return DurationStats{Count: len(s), Median: nearestRank(s, 0.50), P85: nearestRank(s, 0.85)}
}

// nearestRank: el valor en la posición ceil(p·n) (desde 1) de s ordenado.
func nearestRank(s []time.Duration, p float64) time.Duration {
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[max(i, 0)]
}

// FlowWeek son las métricas de una semana [Start, End).
type FlowWeek struct {
	Start, End  time.Time
	LeadTime    DurationStats
	CycleTime   DurationStats
	ClosedValue int
	ClosedOther int
	Canceled    int
	Started     int
	WIP         int
	Source      MetricSource
}

// FlowSummary son las semanas juntas: los conteos suman y las duraciones
// son las de todos los terminados del período. WIP es el de To.
type FlowSummary struct {
	LeadTime    DurationStats
	CycleTime   DurationStats
	ClosedValue int
	ClosedOther int
	Canceled    int
	Started     int
	WIP         int
	Source      MetricSource
}

// StaleIssue es un issue en curso sin actividad reciente.
type StaleIssue struct {
	Number          int32
	Title           string
	StatusID        string
	InProgressSince time.Time
	LastActivityAt  time.Time
	Confidence      MetricConfidence
}

// FlowMetrics es el resultado del cálculo.
type FlowMetrics struct {
	Weeks      []FlowWeek
	Summary    FlowSummary
	Stale      []StaleIssue
	StaleTotal int
	// Issues: los que existen antes de To. DoneWithoutDates: en Done en To
	// sin transición con fecha que lo lleve ahí.
	Issues           int
	DoneWithoutDates int
}

// FlowWeekBounds parte [from, to) en semanas de lunes 00:00 a lunes 00:00
// en loc; la primera y la última se recortan a from y to.
func FlowWeekBounds(from, to time.Time, loc *time.Location) [][2]time.Time {
	var out [][2]time.Time
	f := from.In(loc)
	// Lunes de la semana de from (Weekday: domingo = 0).
	back := (int(f.Weekday()) + 6) % 7
	ws := time.Date(f.Year(), f.Month(), f.Day()-back, 0, 0, 0, 0, loc)
	for ws.Before(to) {
		we := time.Date(ws.Year(), ws.Month(), ws.Day()+7, 0, 0, 0, 0, loc)
		s, e := ws, we
		if s.Before(from) {
			s = from
		}
		if e.After(to) {
			e = to
		}
		out = append(out, [2]time.Time{s, e})
		ws = we
	}
	return out
}

// stateEntry es la entrada de un issue a una categoría. Dated: viene de una
// transición (la primera entrada, la del alta, no).
type stateEntry struct {
	At     time.Time
	State  IssueState
	Source EventSource
	Dated  bool
}

// timeline arma las entradas a cada categoría de un issue (ver el
// comentario de GetFlowMetrics en el proto):
//   - Cuentan las transiciones live, import_history e import_file, y las
//     import_sync posteriores al último evento importado con fecha (o
//     todas si el issue no tiene ninguno).
//   - La categoría inicial es la de origen de la primera transición que
//     cuenta; sin transiciones, la actual.
//   - Una transición a la categoría en la que ya está no hace nada.
func timeline(is *FlowIssue) []stateEntry {
	hasDated, lastDated := datedHorizon(is)
	createdSrc := EventSourceLive
	for _, ev := range is.Events {
		if ev.Created {
			createdSrc = ev.Source.OrLive()
		}
	}
	var accepted []FlowEvent
	for _, ev := range is.Events {
		if counts(ev, hasDated, lastDated) {
			accepted = append(accepted, ev)
		}
	}
	initial := is.State
	if len(accepted) > 0 && accepted[0].From.IsValid() {
		initial = accepted[0].From
	}
	out := []stateEntry{{At: is.CreatedAt, State: initial, Source: createdSrc}}
	cur := initial
	for _, ev := range accepted {
		if ev.To == cur || !ev.To.IsValid() {
			continue
		}
		at := ev.At
		if at.Before(is.CreatedAt) {
			at = is.CreatedAt
		}
		out = append(out, stateEntry{At: at, State: ev.To, Source: ev.Source.OrLive(), Dated: true})
		cur = ev.To
	}
	return out
}

// datedHorizon: si el issue tiene eventos importados con fecha
// (import_history o import_file) y el último de ellos.
func datedHorizon(is *FlowIssue) (bool, time.Time) {
	var last time.Time
	has := false
	for _, ev := range is.Events {
		if s := ev.Source.OrLive(); s == EventSourceImportHistory || s == EventSourceImportFile {
			has = true
			if ev.At.After(last) {
				last = ev.At
			}
		}
	}
	return has, last
}

// counts: la transición entra a la línea de tiempo.
func counts(ev FlowEvent, hasDated bool, lastDated time.Time) bool {
	if !ev.Transition {
		return false
	}
	if ev.Source.OrLive() != EventSourceImportSync {
		return true
	}
	return !hasDated || ev.At.After(lastDated)
}

// entriesBefore son las entradas antes de t (exclusivo), en orden.
func entriesBefore(entries []stateEntry, t time.Time) []stateEntry {
	i := sort.Search(len(entries), func(i int) bool { return !entries[i].At.Before(t) })
	return entries[:i]
}

// firstEntry es la primera entrada a state de entries; false si no hay. Va
// por orden y no por fecha: dos transiciones pueden tener la misma.
func firstEntry(entries []stateEntry, state IssueState) (stateEntry, bool) {
	for _, e := range entries {
		if e.State == state {
			return e, true
		}
	}
	return stateEntry{}, false
}

// lastActivity es el último evento antes de t que cuenta como actividad:
// el alta, cualquier evento que no sea import_sync, y las transiciones
// import_sync que cuentan.
func lastActivity(is *FlowIssue, t time.Time, hasDated bool, lastDated time.Time) time.Time {
	last := is.CreatedAt
	for _, ev := range is.Events {
		if !ev.At.Before(t) {
			continue
		}
		if ev.Source.OrLive() == EventSourceImportSync && !counts(ev, hasDated, lastDated) {
			continue
		}
		if ev.At.After(last) {
			last = ev.At
		}
	}
	return last
}

// ComputeFlowMetrics calcula las métricas de flujo de issues en q.
func ComputeFlowMetrics(issues []FlowIssue, q FlowQuery) FlowMetrics {
	bounds := FlowWeekBounds(q.From, q.To, q.Location)
	weeks := make([]FlowWeek, len(bounds))
	leads := make([][]time.Duration, len(bounds))
	cycles := make([][]time.Duration, len(bounds))
	for i, b := range bounds {
		weeks[i].Start, weeks[i].End = b[0], b[1]
	}
	value := map[string]bool{}
	for _, id := range q.ValueLabelIDs {
		value[id] = true
	}

	var res FlowMetrics
	var stale []StaleIssue
	for k := range issues {
		is := &issues[k]
		if !is.CreatedAt.Before(q.To) {
			continue
		}
		res.Issues++
		entries := timeline(is)
		start, hasStart := firstEntry(entries, IssueStateInProgress)
		isValue := len(value) == 0 || slices.ContainsFunc(is.LabelIDs, func(id string) bool { return value[id] })

		for i, b := range bounds {
			w := &weeks[i]
			before := entriesBefore(entries, b[1])
			if len(before) == 0 {
				continue
			}
			end := before[len(before)-1]
			started := hasStart && start.At.Before(b[1])
			// Cada transición suma una vez a la fuente de la semana, aunque
			// sostenga dos números (la entrada a en curso: cycle time y
			// empezados).
			countedStart := false
			// Terminado o descartado esta semana: la última entrada antes del
			// fin de la semana es a esa categoría, con fecha y dentro de ella.
			if end.Dated && !end.At.Before(b[0]) {
				switch end.State {
				case IssueStateDone:
					if isValue {
						w.ClosedValue++
					} else {
						w.ClosedOther++
					}
					w.Source.add(end.Source)
					leads[i] = append(leads[i], end.At.Sub(is.CreatedAt))
					if started {
						cycles[i] = append(cycles[i], end.At.Sub(start.At))
						w.Source.add(start.Source)
						countedStart = true
					}
				case IssueStateCanceled:
					w.Canceled++
					w.Source.add(end.Source)
				}
			}
			if end.State == IssueStateInProgress {
				w.WIP++
			}
			if started && !start.At.Before(b[0]) {
				w.Started++
				if !countedStart {
					w.Source.add(start.Source)
				}
			}
		}

		untilTo := entriesBefore(entries, q.To)
		atTo := untilTo[len(untilTo)-1]
		switch atTo.State {
		case IssueStateDone:
			if !atTo.Dated {
				res.DoneWithoutDates++
			}
		case IssueStateInProgress:
			res.Summary.WIP++
			hasDated, lastDated := datedHorizon(is)
			last := lastActivity(is, q.To, hasDated, lastDated)
			if atTo.At.After(last) {
				last = atTo.At
			}
			if q.To.Sub(last) >= time.Duration(q.StaleDays)*24*time.Hour {
				stale = append(stale, StaleIssue{
					Number: is.Number, Title: is.Title, StatusID: is.StatusID,
					InProgressSince: atTo.At, LastActivityAt: last, Confidence: confidenceOf(atTo.Source),
				})
			}
		}
	}

	var allLeads, allCycles []time.Duration
	for i := range weeks {
		w := &weeks[i]
		w.LeadTime, w.CycleTime = durationStats(leads[i]), durationStats(cycles[i])
		allLeads = append(allLeads, leads[i]...)
		allCycles = append(allCycles, cycles[i]...)
		s := &res.Summary
		s.ClosedValue += w.ClosedValue
		s.ClosedOther += w.ClosedOther
		s.Canceled += w.Canceled
		s.Started += w.Started
		s.Source.merge(w.Source)
	}
	res.Summary.LeadTime, res.Summary.CycleTime = durationStats(allLeads), durationStats(allCycles)
	res.Weeks = weeks

	sort.SliceStable(stale, func(i, j int) bool {
		if !stale[i].InProgressSince.Equal(stale[j].InProgressSince) {
			return stale[i].InProgressSince.Before(stale[j].InProgressSince)
		}
		return stale[i].Number < stale[j].Number
	})
	res.StaleTotal = len(stale)
	if len(stale) > q.StaleMax {
		stale = stale[:q.StaleMax]
	}
	res.Stale = stale
	return res
}

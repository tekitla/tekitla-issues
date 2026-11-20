package model

import (
	"cmp"
	"slices"
	"sort"
	"time"
)

// Eficiencia del flujo, cola humana y señal de contratación (issue 036,
// parte 3). Salen de la historia de las sesiones de agentes (issue 026). El
// comentario de flow_metrics.proto es la especificación; el cálculo es puro.

// SessionUnreported es el estado de una transición que marca un hueco sin
// reportes (más de stallAfter): desde ahí la sesión no cuenta hasta que
// vuelve a reportar. Solo existe en la historia, nunca en agent_sessions.
const SessionUnreported SessionStatus = "unreported"

// SessionTransition es un cambio guardado en la historia de una sesión: desde
// At, la sesión está en ese estado, espera eso y está ligada a ese issue,
// hasta la transición siguiente.
type SessionTransition struct {
	SessionID     string
	At            time.Time
	Status        SessionStatus
	WaitingOn     WaitingOn
	WaitingReason WaitingReason
	// ProjectID vacío e IssueNumber 0: sin issue.
	ProjectID   string
	IssueNumber int32
}

func transitionOf(s *AgentSession, at time.Time) SessionTransition {
	return SessionTransition{
		SessionID: s.ID, At: at, Status: s.Status, WaitingOn: s.WaitingOn, WaitingReason: s.WaitingReason,
		ProjectID: s.ProjectID, IssueNumber: s.IssueNumber,
	}
}

// sameFlowState: lo que mide la historia no cambió.
func sameFlowState(a, b *AgentSession) bool {
	return a.Status == b.Status && a.WaitingOn == b.WaitingOn && a.WaitingReason == b.WaitingReason &&
		a.ProjectID == b.ProjectID && a.IssueNumber == b.IssueNumber
}

// PlanTransitions decide qué filas de historia escribe un reporte ya
// planeado (out, de PlanReport) sobre las sesiones guardadas (existing).
// history dice qué sesiones ya tienen historia.
//   - Sesión nueva o reabierta: una fila con su estado.
//   - Sesión sin historia (anterior a la tabla): una fila de base con el
//     estado que trae el reporte, aunque no cambie nada.
//   - Hueco: si la sesión no reportó en más de stallAfter, primero una fila
//     unreported en su último reporte y después una con el estado de ahora.
//   - Si no, una fila solo si cambió el estado, la espera, el motivo o el
//     issue.
//   - Una que pasa a ENDED por no venir en el reporte: ended (con el hueco
//     antes, si lo hubo). Sin historia no hace falta.
func PlanTransitions(existing []*AgentSession, history map[string]bool, out ReportOutcome, now time.Time, stallAfter time.Duration) []SessionTransition {
	byID := make(map[string]*AgentSession, len(existing))
	for _, e := range existing {
		byID[e.ID] = e
	}
	gap := func(prev *AgentSession) bool {
		return prev.Status != SessionEnded && now.Sub(prev.LastSeenAt) > stallAfter
	}
	var rows []SessionTransition
	for _, next := range out.Upserts {
		prev := byID[next.ID]
		switch {
		case prev == nil, prev.Status == SessionEnded && next.Status != SessionEnded, !history[prev.ID]:
			rows = append(rows, transitionOf(next, now))
		case gap(prev):
			rows = append(rows,
				SessionTransition{SessionID: prev.ID, At: prev.LastSeenAt, Status: SessionUnreported, WaitingOn: WaitingNone},
				transitionOf(next, now))
		case !sameFlowState(prev, next):
			rows = append(rows, transitionOf(next, now))
		}
	}
	for _, id := range out.EndedIDs {
		prev := byID[id]
		if prev == nil || !history[id] {
			continue
		}
		if gap(prev) {
			rows = append(rows, SessionTransition{SessionID: id, At: prev.LastSeenAt, Status: SessionUnreported, WaitingOn: WaitingNone})
		}
		rows = append(rows, SessionTransition{SessionID: id, At: now, Status: SessionEnded, WaitingOn: WaitingNone})
	}
	return rows
}

// SessionTrack es la historia de una sesión, en orden, con su último
// reporte (para cerrar el último tramo).
type SessionTrack struct {
	SessionID   string
	LastSeenAt  time.Time
	Transitions []SessionTransition
}

// FlowClass es el estado de un issue en un instante, de mayor a menor
// prioridad (el número más chico gana).
type FlowClass int

// Estados de un issue según sus sesiones.
const (
	FlowClassWorking FlowClass = iota
	FlowClassWaitingHuman
	FlowClassBlocked
	FlowClassInReview
	FlowClassIdle
	// flowClassNone: no cuenta (terminada o sin reportes).
	flowClassNone
)

// classOf es el estado de flujo de una sesión en una transición.
func classOf(t SessionTransition) FlowClass {
	switch t.Status {
	case SessionWorking:
		return FlowClassWorking
	case SessionEnded, SessionUnreported:
		return flowClassNone
	}
	switch t.WaitingOn {
	case WaitingPerson:
		return FlowClassWaitingHuman
	case WaitingSession, WaitingOther:
		return FlowClassBlocked
	}
	if t.Status == SessionWaitingOK {
		// esperando-ok sin token: pide un permiso o una respuesta.
		return FlowClassWaitingHuman
	}
	return FlowClassIdle
}

// FlowSegment es un tramo [Start, End) en que un issue está en Class
// (con Reason si es WaitingHuman).
type FlowSegment struct {
	Start, End time.Time
	Class      FlowClass
	Reason     WaitingReason
}

type sessionInterval struct {
	start, end time.Time
	class      FlowClass
	reason     WaitingReason
}

// IssueKey identifica un issue de un proyecto.
type IssueKey struct {
	ProjectID string
	Number    int32
}

// IssueSegments arma, para cada issue que acepta keep, sus tramos de tiempo
// de reloj: en cada instante, el estado de mayor prioridad entre sus
// sesiones. El último tramo de una sesión termina en now, o en su último
// reporte si no reporta hace más de stallAfter. Los tramos van en orden, sin
// solaparse, y dos contiguos iguales se juntan; un hueco es tiempo sin
// sesiones que cuenten.
func IssueSegments(tracks []SessionTrack, keep func(IssueKey) bool, now time.Time, stallAfter time.Duration) map[IssueKey][]FlowSegment {
	byIssue := map[IssueKey][]sessionInterval{}
	for _, tr := range tracks {
		for i, t := range tr.Transitions {
			c := classOf(t)
			if c == flowClassNone || t.ProjectID == "" {
				continue
			}
			k := IssueKey{t.ProjectID, t.IssueNumber}
			if keep != nil && !keep(k) {
				continue
			}
			end := now
			if i+1 < len(tr.Transitions) {
				end = tr.Transitions[i+1].At
			} else if now.Sub(tr.LastSeenAt) > stallAfter {
				end = tr.LastSeenAt
			}
			if end.After(now) {
				end = now
			}
			if !end.After(t.At) {
				continue
			}
			reason := WaitingReasonNone
			if c == FlowClassWaitingHuman {
				reason = t.WaitingReason
			}
			byIssue[k] = append(byIssue[k], sessionInterval{start: t.At, end: end, class: c, reason: reason})
		}
	}
	out := make(map[IssueKey][]FlowSegment, len(byIssue))
	for k, ivs := range byIssue {
		out[k] = sweep(ivs)
	}
	return out
}

// sweep resuelve los intervalos de las sesiones de un issue en tramos sin
// solaparse. Entre varias esperas a una persona a la vez, el motivo es el de
// la que empezó antes.
func sweep(ivs []sessionInterval) []FlowSegment {
	var points []time.Time
	for _, iv := range ivs {
		points = append(points, iv.start, iv.end)
	}
	slices.SortFunc(points, func(a, b time.Time) int { return a.Compare(b) })
	points = slices.CompactFunc(points, func(a, b time.Time) bool { return a.Equal(b) })
	sort.SliceStable(ivs, func(i, j int) bool { return ivs[i].start.Before(ivs[j].start) })
	var segs []FlowSegment
	for p := 0; p+1 < len(points); p++ {
		a, b := points[p], points[p+1]
		best := flowClassNone
		reason := WaitingReasonNone
		for _, iv := range ivs {
			if iv.start.After(a) || iv.end.Before(b) {
				continue
			}
			if iv.class < best {
				best, reason = iv.class, iv.reason
			}
		}
		if best == flowClassNone {
			continue
		}
		if n := len(segs); n > 0 && segs[n-1].End.Equal(a) && segs[n-1].Class == best && segs[n-1].Reason == reason {
			segs[n-1].End = b
			continue
		}
		segs = append(segs, FlowSegment{Start: a, End: b, Class: best, Reason: reason})
	}
	return segs
}

// FlowEfficiency es dónde se fue el tiempo de los issues en una ventana.
type FlowEfficiency struct {
	Working, WaitingHuman, Blocked, InReview, Idle time.Duration
	Issues                                         int
	// ByReason reparte WaitingHuman por motivo (con WaitingReasonNone).
	ByReason map[WaitingReason]time.Duration
}

// Measured es el tiempo que entra en la eficiencia.
func (e FlowEfficiency) Measured() time.Duration {
	return e.Working + e.WaitingHuman + e.Blocked + e.InReview
}

// EfficiencyBP es working / medido × 10 000, redondeado hacia abajo; false
// si no hay tiempo medido.
func (e FlowEfficiency) EfficiencyBP() (int, bool) {
	m := e.Measured()
	if m <= 0 {
		return 0, false
	}
	return int(int64(e.Working) * 10000 / int64(m)), true
}

// Empty informa si no hay ningún tiempo.
func (e FlowEfficiency) Empty() bool { return e.Measured()+e.Idle == 0 }

// ReasonTimes es ByReason de mayor a menor (desempate por motivo).
func (e FlowEfficiency) ReasonTimes() []ReasonTime {
	out := make([]ReasonTime, 0, len(e.ByReason))
	for r, d := range e.ByReason {
		if d > 0 {
			out = append(out, ReasonTime{Reason: r, Duration: d})
		}
	}
	slices.SortFunc(out, func(a, b ReasonTime) int {
		if c := cmp.Compare(b.Duration, a.Duration); c != 0 {
			return c
		}
		return reasonOrder(a.Reason) - reasonOrder(b.Reason)
	})
	return out
}

// ReasonTime es el tiempo de espera a una persona con un motivo.
type ReasonTime struct {
	Reason   WaitingReason
	Duration time.Duration
}

func reasonOrder(r WaitingReason) int {
	return slices.Index([]WaitingReason{WaitingReasonReview, WaitingReasonTesting, WaitingReasonDecision, WaitingReasonDeploy, WaitingReasonNone}, r)
}

func (e *FlowEfficiency) add(o FlowEfficiency) {
	e.Working += o.Working
	e.WaitingHuman += o.WaitingHuman
	e.Blocked += o.Blocked
	e.InReview += o.InReview
	e.Idle += o.Idle
	e.Issues += o.Issues
	for r, d := range o.ByReason {
		if e.ByReason == nil {
			e.ByReason = map[WaitingReason]time.Duration{}
		}
		e.ByReason[r] += d
	}
}

// EfficiencyIn suma el tiempo de los tramos dentro de [from, to). Issues
// cuenta los issues con algún tiempo.
func EfficiencyIn(segs map[IssueKey][]FlowSegment, from, to time.Time) FlowEfficiency {
	var e FlowEfficiency
	for _, list := range segs {
		touched := false
		for _, s := range list {
			a, b := s.Start, s.End
			if a.Before(from) {
				a = from
			}
			if b.After(to) {
				b = to
			}
			if !b.After(a) {
				continue
			}
			touched = true
			d := b.Sub(a)
			switch s.Class {
			case FlowClassWorking:
				e.Working += d
			case FlowClassWaitingHuman:
				e.WaitingHuman += d
				if e.ByReason == nil {
					e.ByReason = map[WaitingReason]time.Duration{}
				}
				e.ByReason[s.Reason] += d
			case FlowClassBlocked:
				e.Blocked += d
			case FlowClassInReview:
				e.InReview += d
			case FlowClassIdle:
				e.Idle += d
			}
		}
		if touched {
			e.Issues++
		}
	}
	return e
}

// humanWait es un tramo continuo de espera a una persona de un issue.
// Answered: terminó porque el issue pasó a otro estado (no por un hueco).
// Open: sigue en now.
type humanWait struct {
	start, end     time.Time
	answered, open bool
}

func humanWaits(segs map[IssueKey][]FlowSegment, now time.Time) []humanWait {
	var out []humanWait
	for _, list := range segs {
		for i := 0; i < len(list); i++ {
			if list[i].Class != FlowClassWaitingHuman {
				continue
			}
			w := humanWait{start: list[i].Start, end: list[i].End}
			// Un cambio de motivo sigue siendo la misma espera.
			for i+1 < len(list) && list[i+1].Class == FlowClassWaitingHuman && list[i+1].Start.Equal(w.end) {
				i++
				w.end = list[i].End
			}
			w.answered = i+1 < len(list) && list[i+1].Start.Equal(w.end)
			w.open = !w.answered && w.end.Equal(now)
			out = append(out, w)
		}
	}
	return out
}

// HumanQueue son las esperas a una persona en una ventana.
type HumanQueue struct {
	// Pending: issues esperando en el fin de la ventana; OldestPending, la
	// más vieja (vale si Pending > 0).
	Pending       int
	OldestPending time.Duration
	// Response: las esperas respondidas dentro de la ventana.
	Response DurationStats
	// responses: las muestras de Response (para sumar ventanas).
	responses []time.Duration
}

// HumanQueueIn calcula la cola humana de [from, to).
func HumanQueueIn(segs map[IssueKey][]FlowSegment, from, to, now time.Time) HumanQueue {
	var q HumanQueue
	for _, w := range humanWaits(segs, now) {
		if w.answered && !w.end.Before(from) && w.end.Before(to) {
			q.responses = append(q.responses, w.end.Sub(w.start))
		}
		if w.start.Before(to) && (w.end.After(to) || (w.open && !w.end.Before(to))) {
			q.Pending++
			q.OldestPending = max(q.OldestPending, to.Sub(w.start))
		}
	}
	q.Response = durationStats(q.responses)
	return q
}

// RecordedSince es la primera transición guardada de una sesión ligada a
// projectID (y, con number > 0, a ese issue); false si no hay.
func RecordedSince(tracks []SessionTrack, projectID string, number int32) (time.Time, bool) {
	var first time.Time
	ok := false
	for _, tr := range tracks {
		for _, t := range tr.Transitions {
			if t.ProjectID != projectID || (number > 0 && t.IssueNumber != number) {
				continue
			}
			if !ok || t.At.Before(first) {
				first, ok = t.At, true
			}
		}
	}
	return first, ok
}

// SessionWeek es la eficiencia y la cola humana de una semana. Has: la
// semana termina después de que empezó la historia de sesiones.
type SessionWeek struct {
	Has        bool
	Efficiency FlowEfficiency
	Queue      HumanQueue
}

// SessionFlow es la parte de sesiones de GetFlowMetrics.
type SessionFlow struct {
	Since    time.Time
	HasSince bool
	Weeks    []SessionWeek
	// Summary: suma de las semanas con Has; la cola en to y las respuestas
	// de todo el período. HasSummary: alguna semana tiene.
	Summary      FlowEfficiency
	SummaryQueue HumanQueue
	HasSummary   bool
}

// ComputeSessionWeeks calcula la eficiencia y la cola de cada semana de
// bounds. Las semanas que terminan antes de since no tienen.
func ComputeSessionWeeks(segs map[IssueKey][]FlowSegment, bounds [][2]time.Time, since time.Time, hasSince bool, now time.Time) SessionFlow {
	sf := SessionFlow{Since: since, HasSince: hasSince, Weeks: make([]SessionWeek, len(bounds))}
	if !hasSince || len(bounds) == 0 {
		return sf
	}
	var responses []time.Duration
	for i, b := range bounds {
		if !b[1].After(since) {
			continue
		}
		w := SessionWeek{Has: true, Efficiency: EfficiencyIn(segs, b[0], b[1]), Queue: HumanQueueIn(segs, b[0], b[1], now)}
		sf.Weeks[i] = w
		sf.HasSummary = true
		sf.Summary.add(w.Efficiency)
		responses = append(responses, w.Queue.responses...)
	}
	if sf.HasSummary {
		// Issues del período: los distintos, no la suma de las semanas.
		sf.Summary.Issues = EfficiencyIn(segs, bounds[0][0], bounds[len(bounds)-1][1]).Issues
		to := bounds[len(bounds)-1][1]
		q := HumanQueueIn(segs, to, to, now)
		q.responses = responses
		q.Response = durationStats(responses)
		sf.SummaryQueue = q
	}
	return sf
}

// Señal de contratación (flow_metrics.proto, HiringSignal).
const (
	SignalWeeksNeeded            = 8
	SignalWeeksNeededPreliminary = 4
	signalWindowWeeks            = 4
	// Umbrales en porcentaje.
	signalNeedsPeopleHumanPct = 30
	signalReduceWIPHumanPct   = 15
	signalGrowthPct           = 25
)

// HiringVerdict es lo que dice la señal.
type HiringVerdict int

// Veredictos (mismo orden que el enum del proto).
const (
	HiringVerdictUnspecified HiringVerdict = iota
	HiringVerdictInsufficientData
	HiringVerdictNoSignal
	HiringVerdictNeedsPeople
	HiringVerdictReduceWIP
)

// SignalWindow son los números de 4 semanas.
type SignalWindow struct {
	Start, End    time.Time
	Efficiency    FlowEfficiency
	HumanResponse DurationStats
	WIPMedian     int
	Closed        int
}

// ProfileShare es la parte de la espera a personas con un motivo.
type ProfileShare struct {
	Reason  WaitingReason
	ShareBP int
}

// HiringSignal es la señal de contratación.
type HiringSignal struct {
	Verdict     HiringVerdict
	LiveWeeks   int
	Preliminary bool
	WIPHigh     bool
	Profiles    []ProfileShare
	// Current y Previous: nil si no aplican.
	Current, Previous *SignalWindow
}

// SignalBounds son los extremos de las 8 semanas completas antes de to:
// [from, end), con end el lunes 00:00 de la semana de to (o to, si es lunes
// 00:00).
func SignalBounds(to time.Time, loc *time.Location) (time.Time, time.Time) {
	end := WeekStart(to, loc)
	// Un cambio de horario mueve una hora: el mediodía cae en el lunes.
	from := WeekStart(end.Add(-time.Duration(SignalWeeksNeeded)*7*24*time.Hour+12*time.Hour), loc)
	return from, end
}

// WeekStart es el lunes 00:00 (en loc) de la semana de t.
func WeekStart(t time.Time, loc *time.Location) time.Time {
	f := t.In(loc)
	back := (int(f.Weekday()) + 6) % 7
	return dayStart(time.Date(f.Year(), f.Month(), f.Day()-back, 12, 0, 0, 0, time.UTC), loc)
}

// ComputeHiringSignal arma la señal con las 8 semanas completas antes de to:
// flowWeeks son sus métricas de estados (WIP y cerrados) y segs los tramos de
// sesiones. since: el inicio de la historia de sesiones del proyecto.
func ComputeHiringSignal(flowWeeks []FlowWeek, segs map[IssueKey][]FlowSegment, since time.Time, hasSince bool, now time.Time) HiringSignal {
	var sig HiringSignal
	if hasSince {
		for _, w := range flowWeeks {
			if !w.Start.Before(since) {
				sig.LiveWeeks++
			}
		}
	}
	if sig.LiveWeeks < SignalWeeksNeededPreliminary || len(flowWeeks) < SignalWeeksNeeded {
		sig.Verdict = HiringVerdictInsufficientData
		return sig
	}
	sig.Preliminary = sig.LiveWeeks < SignalWeeksNeeded
	window := func(ws []FlowWeek) *SignalWindow {
		from, to := ws[0].Start, ws[len(ws)-1].End
		w := &SignalWindow{Start: from, End: to, Efficiency: EfficiencyIn(segs, from, to), HumanResponse: HumanQueueIn(segs, from, to, now).Response}
		wips := make([]int, len(ws))
		for i, fw := range ws {
			wips[i] = fw.WIP
			w.Closed += fw.ClosedValue + fw.ClosedOther
		}
		slices.Sort(wips)
		w.WIPMedian = wips[(len(wips)+1)/2-1]
		return w
	}
	n := len(flowWeeks)
	cur := window(flowWeeks[n-signalWindowWeeks:])
	sig.Current = cur
	var prev *SignalWindow
	if !sig.Preliminary {
		prev = window(flowWeeks[n-2*signalWindowWeeks : n-signalWindowWeeks])
		sig.Previous = prev
	}

	e := cur.Efficiency
	measured := int64(e.Measured())
	human := int64(e.WaitingHuman)
	sig.WIPHigh = cur.WIPMedian > 0 && cur.WIPMedian*2 >= cur.Closed
	if prev != nil && grew(int64(prev.WIPMedian), int64(cur.WIPMedian)) {
		sig.WIPHigh = true
	}
	needs := measured > 0 && human*100 >= signalNeedsPeopleHumanPct*measured && e.Working <= e.WaitingHuman
	if needs && prev != nil {
		needs = grew(int64(prev.Efficiency.WaitingHuman), human) ||
			grew(int64(prev.HumanResponse.Median), int64(cur.HumanResponse.Median))
	}
	reduce := human*100 < signalReduceWIPHumanPct*measured && sig.WIPHigh
	switch {
	case needs:
		sig.Verdict = HiringVerdictNeedsPeople
	case reduce:
		sig.Verdict = HiringVerdictReduceWIP
	default:
		sig.Verdict = HiringVerdictNoSignal
	}
	if human > 0 {
		for _, rt := range e.ReasonTimes() {
			if rt.Reason == WaitingReasonNone {
				continue
			}
			sig.Profiles = append(sig.Profiles, ProfileShare{Reason: rt.Reason, ShareBP: int(int64(rt.Duration) * 10000 / human)})
		}
	}
	return sig
}

// grew: cur creció signalGrowthPct % o más contra prev (de 0 a algo es
// crecer).
func grew(prev, cur int64) bool {
	if prev <= 0 {
		return cur > 0
	}
	return cur*100 >= prev*(100+signalGrowthPct)
}

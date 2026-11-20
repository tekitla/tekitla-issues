package model

import (
	"testing"
	"time"
)

// Historia de sesiones, eficiencia del flujo, cola humana y señal de
// contratación (issue 036, parte 3). Datos inventados.

const pTK = "p-tk"

func tr(at string, st SessionStatus, w WaitingOn, reason WaitingReason, number int32) SessionTransition {
	t := SessionTransition{At: ts(at), Status: st, WaitingOn: w, WaitingReason: reason}
	if number > 0 {
		t.ProjectID, t.IssueNumber = pTK, number
	}
	return t
}

func track(id, lastSeen string, ts ...SessionTransition) SessionTrack {
	for i := range ts {
		ts[i].SessionID = id
	}
	return SessionTrack{SessionID: id, LastSeenAt: tsOr(lastSeen), Transitions: ts}
}

func tsOr(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	return ts(s)
}

const stall = 10 * time.Minute

func segsOf(now string, tracks ...SessionTrack) map[IssueKey][]FlowSegment {
	return IssueSegments(tracks, nil, ts(now), stall)
}

// CU-036-6: 3 h en trabajando y 5 h en esperando-ok sobre un issue dan una
// eficiencia de 3/8 (3750 puntos base) y 5 h de espera a una persona.
func TestEfficiency_CU0366(t *testing.T) {
	segs := segsOf("2026-12-01T20:00:00Z",
		track("s1", "2026-12-01T18:00:00Z",
			tr("2026-12-01T10:00:00Z", SessionWorking, WaitingNone, "", 7),
			tr("2026-12-01T13:00:00Z", SessionWaitingOK, WaitingNone, "", 7),
			tr("2026-12-01T18:00:00Z", SessionEnded, WaitingNone, "", 7),
		))
	e := EfficiencyIn(segs, ts("2026-12-01T00:00:00Z"), ts("2026-12-02T00:00:00Z"))
	bp, ok := e.EfficiencyBP()
	if !ok || bp != 3750 || e.Working != 3*time.Hour || e.WaitingHuman != 5*time.Hour || e.Issues != 1 {
		t.Fatalf("efficiency: bp %d (%v), %+v", bp, ok, e)
	}
	// Una ventana que corta los tramos.
	e = EfficiencyIn(segs, ts("2026-12-01T12:00:00Z"), ts("2026-12-01T14:00:00Z"))
	if e.Working != time.Hour || e.WaitingHuman != time.Hour {
		t.Errorf("window: %+v", e)
	}
	// Sin tiempo medido no hay eficiencia.
	if _, ok := (FlowEfficiency{Idle: time.Hour}).EfficiencyBP(); ok {
		t.Error("only idle: want no efficiency")
	}
}

// Tiempo de reloj con prioridad: trabajando > espera a una persona >
// bloqueado > en reposo. Dos sesiones trabajando a la vez cuentan una vez.
func TestEfficiency_ClockTimeAndPriority(t *testing.T) {
	segs := segsOf("2026-12-01T20:00:00Z",
		track("a", "2026-12-01T20:00:00Z",
			tr("2026-12-01T10:00:00Z", SessionWorking, WaitingNone, "", 1),
			tr("2026-12-01T11:00:00Z", SessionEnded, WaitingNone, "", 1)),
		track("b", "2026-12-01T20:00:00Z",
			tr("2026-12-01T10:00:00Z", SessionWorking, WaitingNone, "", 1),
			tr("2026-12-01T10:30:00Z", SessionIdle, WaitingPerson, WaitingReasonReview, 1),
			tr("2026-12-01T12:00:00Z", SessionEnded, WaitingNone, "", 1)),
		track("c", "2026-12-01T20:00:00Z",
			tr("2026-12-01T11:30:00Z", SessionWaitingOK, WaitingSession, "", 1),
			tr("2026-12-01T13:00:00Z", SessionIdle, WaitingNone, "", 1),
			tr("2026-12-01T14:00:00Z", SessionEnded, WaitingNone, "", 1)),
	)
	e := EfficiencyIn(segs, ts("2026-12-01T00:00:00Z"), ts("2026-12-02T00:00:00Z"))
	want := FlowEfficiency{Working: time.Hour, WaitingHuman: time.Hour, Blocked: time.Hour, Idle: time.Hour}
	if e.Working != want.Working || e.WaitingHuman != want.WaitingHuman || e.Blocked != want.Blocked || e.Idle != want.Idle {
		t.Fatalf("got %+v, want %+v", e, want)
	}
	if e.ByReason[WaitingReasonReview] != time.Hour {
		t.Errorf("by reason: %v", e.ByReason)
	}
}

// Un hueco sin reportes y una sesión que dejó de reportar no cuentan; una
// sesión sin issue tampoco.
func TestEfficiency_GapsAndUnlinked(t *testing.T) {
	segs := segsOf("2026-12-01T20:00:00Z",
		track("s1", "2026-12-01T15:00:00Z",
			tr("2026-12-01T10:00:00Z", SessionWorking, WaitingNone, "", 2),
			tr("2026-12-01T11:00:00Z", SessionUnreported, WaitingNone, "", 0),
			tr("2026-12-01T14:00:00Z", SessionWorking, WaitingNone, "", 2)),
		// Sigue «trabajando», pero su último reporte es de las 15:00 (colgada).
		track("s2", "2026-12-01T09:00:00Z",
			tr("2026-12-01T08:00:00Z", SessionWorking, WaitingNone, "", 0)),
	)
	e := EfficiencyIn(segs, ts("2026-12-01T00:00:00Z"), ts("2026-12-02T00:00:00Z"))
	if e.Working != 2*time.Hour || e.Issues != 1 {
		t.Fatalf("got %+v, want 2 h of work on one issue", e)
	}
	// Reportando al día: el último tramo llega hasta ahora.
	segs = segsOf("2026-12-01T20:00:00Z",
		track("s3", "2026-12-01T19:58:00Z", tr("2026-12-01T19:00:00Z", SessionWorking, WaitingNone, "", 3)))
	if e := EfficiencyIn(segs, ts("2026-12-01T00:00:00Z"), ts("2026-12-02T00:00:00Z")); e.Working != time.Hour {
		t.Errorf("fresh session: %+v", e)
	}
}

// Cola humana: una espera respondida, una abierta y una cortada por un hueco.
func TestHumanQueue(t *testing.T) {
	now := "2026-12-03T12:00:00Z"
	segs := segsOf(now,
		// Respondida: espera de 2 h (con un cambio de motivo en medio).
		track("a", now,
			tr("2026-12-01T10:00:00Z", SessionIdle, WaitingPerson, WaitingReasonDecision, 1),
			tr("2026-12-01T11:00:00Z", SessionIdle, WaitingPerson, WaitingReasonReview, 1),
			tr("2026-12-01T12:00:00Z", SessionWorking, WaitingNone, "", 1),
			tr("2026-12-01T13:00:00Z", SessionEnded, WaitingNone, "", 1)),
		// Abierta desde hace 4 h.
		track("b", now, tr("2026-12-03T08:00:00Z", SessionWaitingOK, WaitingNone, "", 2)),
		// Cortada: la máquina dejó de reportar; no es respuesta.
		track("c", "2026-12-02T10:00:00Z", tr("2026-12-02T09:00:00Z", SessionIdle, WaitingPerson, "", 3)),
	)
	q := HumanQueueIn(segs, ts("2026-11-30T00:00:00Z"), ts(now), ts(now))
	if q.Pending != 1 || q.OldestPending != 4*time.Hour || q.Response.Count != 1 || q.Response.Median != 2*time.Hour {
		t.Fatalf("queue: %+v", q)
	}
	// Al final del 2026-12-02: ninguna pendiente (la cortada terminó a las 10).
	q = HumanQueueIn(segs, ts("2026-12-02T00:00:00Z"), ts("2026-12-02T23:00:00Z"), ts(now))
	if q.Pending != 0 || q.Response.Count != 0 {
		t.Errorf("day 2: %+v", q)
	}
	// Una espera que cruza un hueco (laptop cerrada de noche) sigue siendo la
	// misma: respondida a las 10:00, dura 16 h.
	night := segsOf(now,
		track("n", now,
			tr("2026-12-01T18:00:00Z", SessionIdle, WaitingPerson, WaitingReasonReview, 5),
			tr("2026-12-01T19:00:00Z", SessionUnreported, WaitingNone, "", 0),
			tr("2026-12-02T09:00:00Z", SessionIdle, WaitingPerson, WaitingReasonReview, 5),
			tr("2026-12-02T10:00:00Z", SessionWorking, WaitingNone, "", 5),
			tr("2026-12-02T11:00:00Z", SessionEnded, WaitingNone, "", 5)))
	nq := HumanQueueIn(night, ts("2026-12-01T00:00:00Z"), ts("2026-12-03T00:00:00Z"), ts(now))
	if nq.Response.Count != 1 || nq.Response.Median != 16*time.Hour {
		t.Errorf("wait across a gap: %+v", nq)
	}
	if e := EfficiencyIn(night, ts("2026-12-01T00:00:00Z"), ts("2026-12-03T00:00:00Z")); e.WaitingHuman != 2*time.Hour {
		t.Errorf("the gap does not add waiting hours: %+v", e)
	}

	// A media espera de la respondida: pendiente.
	q = HumanQueueIn(segs, ts("2026-12-01T00:00:00Z"), ts("2026-12-01T11:30:00Z"), ts(now))
	if q.Pending != 1 || q.OldestPending != 90*time.Minute {
		t.Errorf("mid wait: %+v", q)
	}
}

func TestPlanTransitions(t *testing.T) {
	now := ts("2026-12-01T12:00:00Z")
	fresh := now.Add(-time.Minute)
	old := now.Add(-time.Hour)
	sess := func(id string, st SessionStatus, w WaitingOn, seen time.Time) *AgentSession {
		return &AgentSession{ID: id, Status: st, WaitingOn: w, LastSeenAt: seen, ProjectID: pTK, IssueNumber: 1}
	}
	existing := []*AgentSession{
		sess("same", SessionWorking, WaitingNone, fresh),
		sess("changed", SessionWorking, WaitingNone, fresh),
		sess("gap", SessionIdle, WaitingNone, old),
		sess("nohist", SessionIdle, WaitingNone, fresh),
		sess("gone", SessionWaitingOK, WaitingNone, old),
		sess("gone-nohist", SessionIdle, WaitingNone, fresh),
		sess("reopen", SessionEnded, WaitingNone, old),
	}
	history := map[string]bool{"same": true, "changed": true, "gap": true, "gone": true, "reopen": true}
	next := func(id string, st SessionStatus, w WaitingOn, r WaitingReason) *AgentSession {
		s := sess(id, st, w, now)
		s.WaitingReason = r
		return s
	}
	out := ReportOutcome{
		Upserts: []*AgentSession{
			next("same", SessionWorking, WaitingNone, ""),
			next("changed", SessionIdle, WaitingPerson, WaitingReasonTesting),
			next("gap", SessionIdle, WaitingNone, ""),
			next("nohist", SessionIdle, WaitingNone, ""),
			next("new", SessionWorking, WaitingNone, ""),
			next("reopen", SessionWorking, WaitingNone, ""),
		},
		EndedIDs: []string{"gone", "gone-nohist"},
	}
	got := PlanTransitions(existing, history, out, now, stall)
	type row struct {
		id string
		at time.Time
		st SessionStatus
	}
	want := []row{
		{"changed", now, SessionIdle},
		{"gap", old, SessionUnreported}, {"gap", now, SessionIdle},
		{"nohist", now, SessionIdle},
		{"new", now, SessionWorking},
		{"reopen", now, SessionWorking},
		{"gone", old, SessionUnreported}, {"gone", now, SessionEnded},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].SessionID != w.id || !got[i].At.Equal(w.at) || got[i].Status != w.st {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], w)
		}
	}
	if got[0].WaitingOn != WaitingPerson || got[0].WaitingReason != WaitingReasonTesting || got[0].IssueNumber != 1 {
		t.Errorf("changed row: %+v", got[0])
	}
}

// signalFixture arma 8 semanas completas (lunes UTC) con el WIP y los
// cerrados dados, y una sesión por semana con las horas de trabajo y de
// espera a una persona dadas.
func signalFixture(wip, closed []int, work, wait []time.Duration, reason WaitingReason) ([]FlowWeek, map[IssueKey][]FlowSegment) {
	start := ts("2026-12-07T00:00:00Z")
	weeks := make([]FlowWeek, 8)
	segs := map[IssueKey][]FlowSegment{}
	k := IssueKey{pTK, 1}
	for i := range weeks {
		ws := start.Add(time.Duration(i) * 7 * 24 * time.Hour)
		weeks[i] = FlowWeek{Start: ws, End: ws.Add(7 * 24 * time.Hour), WIP: wip[i], ClosedValue: closed[i]}
		t0 := ws.Add(10 * time.Hour)
		if work[i] > 0 {
			segs[k] = append(segs[k], FlowSegment{Start: t0, End: t0.Add(work[i]), Class: FlowClassWorking})
			t0 = t0.Add(work[i])
		}
		if wait[i] > 0 {
			segs[k] = append(segs[k], FlowSegment{Start: t0, End: t0.Add(wait[i]), Class: FlowClassWaitingHuman, Reason: reason})
			// Respondida: vuelve a trabajar un minuto.
			segs[k] = append(segs[k], FlowSegment{Start: t0.Add(wait[i]), End: t0.Add(wait[i] + time.Minute), Class: FlowClassWorking})
		}
	}
	return weeks, segs
}

func repeat[T any](v T, n int) []T {
	out := make([]T, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestHiringSignal(t *testing.T) {
	now := ts("2027-02-01T00:00:00Z")
	h := time.Hour
	since := ts("2026-12-07T00:00:00Z")

	// CU-036-7: la espera a personas crece y es la mayor parte; los agentes
	// trabajan menos de lo que esperan. Falta gente, perfil: revisión.
	weeks, segs := signalFixture(repeat(3, 8), repeat(3, 8),
		repeat(4*h, 8), []time.Duration{3 * h, 3 * h, 3 * h, 3 * h, 8 * h, 8 * h, 8 * h, 8 * h}, WaitingReasonReview)
	sig := ComputeHiringSignal(weeks, segs, since, true, now)
	if sig.Verdict != HiringVerdictNeedsPeople || sig.Preliminary || sig.LiveWeeks != 8 || sig.Current == nil || sig.Previous == nil {
		t.Fatalf("CU-036-7: %+v", sig)
	}
	if len(sig.Profiles) != 1 || sig.Profiles[0].Reason != WaitingReasonReview || sig.Profiles[0].ShareBP != 10000 {
		t.Errorf("CU-036-7 profiles: %+v", sig.Profiles)
	}
	if sig.Current.Efficiency.WaitingHuman != 32*h || sig.Previous.Efficiency.WaitingHuman != 12*h {
		t.Errorf("CU-036-7 numbers: current %+v, previous %+v", sig.Current.Efficiency, sig.Previous.Efficiency)
	}

	// La misma espera, pero sin crecer: no es señal.
	weeks, segs = signalFixture(repeat(3, 8), repeat(3, 8), repeat(4*h, 8), repeat(8*h, 8), WaitingReasonReview)
	if sig := ComputeHiringSignal(weeks, segs, since, true, now); sig.Verdict != HiringVerdictNoSignal {
		t.Errorf("flat wait: %+v", sig.Verdict)
	}

	// CU-036-8: WIP alto (10 en curso, 2 cerrados por semana) y sin espera
	// humana: bajar el WIP, no contratar.
	weeks, segs = signalFixture(repeat(10, 8), repeat(2, 8), repeat(10*h, 8), repeat(time.Duration(0), 8), "")
	sig = ComputeHiringSignal(weeks, segs, since, true, now)
	if sig.Verdict != HiringVerdictReduceWIP || !sig.WIPHigh || len(sig.Profiles) != 0 {
		t.Fatalf("CU-036-8: %+v", sig)
	}

	// Menos de 4 semanas con historia: datos insuficientes.
	sig = ComputeHiringSignal(weeks, segs, ts("2027-01-11T00:00:00Z"), true, now)
	if sig.Verdict != HiringVerdictInsufficientData || sig.LiveWeeks != 3 || sig.Current != nil {
		t.Errorf("insufficient: %+v", sig)
	}
	if sig := ComputeHiringSignal(weeks, segs, time.Time{}, false, now); sig.Verdict != HiringVerdictInsufficientData || sig.LiveWeeks != 0 {
		t.Errorf("no history: %+v", sig)
	}

	// 5 semanas: lectura preliminar, sin mes anterior.
	sig = ComputeHiringSignal(weeks, segs, ts("2026-12-28T00:00:00Z"), true, now)
	if !sig.Preliminary || sig.LiveWeeks != 5 || sig.Previous != nil || sig.Verdict != HiringVerdictReduceWIP {
		t.Errorf("preliminary: %+v", sig)
	}
}

func TestSignalBoundsAndWeekStart(t *testing.T) {
	loc, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		t.Fatal(err)
	}
	// Miércoles 2027-02-03 en CDMX: la última semana completa termina el
	// lunes 2027-02-01 00:00 y la primera empieza 8 semanas antes.
	from, end := SignalBounds(time.Date(2027, 2, 3, 15, 0, 0, 0, loc), loc)
	if !end.Equal(time.Date(2027, 2, 1, 0, 0, 0, 0, loc)) || !from.Equal(time.Date(2026, 12, 7, 0, 0, 0, 0, loc)) {
		t.Errorf("bounds: %v – %v", from, end)
	}
	// Un to que es lunes 00:00: esa semana ya está completa.
	monday := time.Date(2027, 2, 1, 0, 0, 0, 0, loc)
	if _, end := SignalBounds(monday, loc); !end.Equal(monday) {
		t.Errorf("monday: %v", end)
	}
	if got := len(FlowWeekBounds(from, end, loc)); got != 8 {
		t.Errorf("weeks: %d", got)
	}
}

// ComputeSessionWeeks: sin historia antes de una semana, esa semana no trae
// datos; el resumen suma y cuenta issues distintos.
func TestComputeSessionWeeks(t *testing.T) {
	now := ts("2026-12-16T00:00:00Z")
	segs := segsOf("2026-12-16T00:00:00Z",
		track("s1", "2026-12-15T00:00:00Z",
			tr("2026-12-08T10:00:00Z", SessionWorking, WaitingNone, "", 4),
			tr("2026-12-08T12:00:00Z", SessionWaitingOK, WaitingNone, "", 4),
			tr("2026-12-08T13:00:00Z", SessionEnded, WaitingNone, "", 4)),
		track("s2", "2026-12-15T00:00:00Z",
			tr("2026-12-14T10:00:00Z", SessionWorking, WaitingNone, "", 4),
			tr("2026-12-14T11:00:00Z", SessionEnded, WaitingNone, "", 4)),
	)
	bounds := [][2]time.Time{
		{ts("2026-11-30T00:00:00Z"), ts("2026-12-07T00:00:00Z")},
		{ts("2026-12-07T00:00:00Z"), ts("2026-12-14T00:00:00Z")},
		{ts("2026-12-14T00:00:00Z"), ts("2026-12-16T00:00:00Z")},
	}
	sf := ComputeSessionWeeks(segs, bounds, ts("2026-12-08T10:00:00Z"), true, now)
	if sf.Weeks[0].Has || !sf.Weeks[1].Has || !sf.Weeks[2].Has {
		t.Fatalf("has: %+v", sf.Weeks)
	}
	if sf.Summary.Working != 3*time.Hour || sf.Summary.WaitingHuman != time.Hour || sf.Summary.Issues != 1 {
		t.Errorf("summary: %+v", sf.Summary)
	}
	if sf.SummaryQueue.Response.Count != 0 || sf.SummaryQueue.Pending != 0 {
		// La espera del 2026-12-08 terminó porque la sesión terminó: no es
		// una respuesta (la sesión no siguió trabajando en el issue).
		t.Errorf("summary queue: %+v", sf.SummaryQueue)
	}
	if none := ComputeSessionWeeks(segs, bounds, time.Time{}, false, now); none.HasSummary || none.Weeks[1].Has {
		t.Errorf("without history: %+v", none)
	}
}

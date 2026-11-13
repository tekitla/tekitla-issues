package model

import (
	"testing"
	"time"
)

// Datos inventados: ningún issue sale de un proyecto real.

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func move(at string, src EventSource, from, to IssueState) FlowEvent {
	return FlowEvent{At: ts(at), Source: src, Transition: true, From: from, To: to}
}

func created(at string, src EventSource) FlowEvent {
	return FlowEvent{At: ts(at), Source: src, Created: true}
}

func note(at string, src EventSource) FlowEvent {
	return FlowEvent{At: ts(at), Source: src}
}

const (
	todo     = IssueStateTodo
	doing    = IssueStateInProgress
	done     = IssueStateDone
	canceled = IssueStateCanceled
	live     = EventSourceLive
	hist     = EventSourceImportHistory
	file     = EventSourceImportFile
	sync     = EventSourceImportSync
)

// Semanas de 2026-06: lunes 1, 8, 15, 22 y 29.
func query(from, to string) FlowQuery {
	return FlowQuery{From: ts(from), To: ts(to), Location: time.UTC, StaleDays: 30, StaleMax: 20}
}

func TestFlowWeekBounds(t *testing.T) {
	b := FlowWeekBounds(ts("2026-06-03T12:00:00Z"), ts("2026-06-17T00:00:00Z"), time.UTC)
	want := [][2]string{
		{"2026-06-03T12:00:00Z", "2026-06-08T00:00:00Z"},
		{"2026-06-08T00:00:00Z", "2026-06-15T00:00:00Z"},
		{"2026-06-15T00:00:00Z", "2026-06-17T00:00:00Z"},
	}
	if len(b) != len(want) {
		t.Fatalf("semanas = %d, quiero %d", len(b), len(want))
	}
	for i, w := range want {
		if !b[i][0].Equal(ts(w[0])) || !b[i][1].Equal(ts(w[1])) {
			t.Errorf("semana %d = %v, quiero %v", i, b[i], w)
		}
	}
}

func TestFlowWeekBounds_zonaHoraria(t *testing.T) {
	mx, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		t.Skip("sin tzdata:", err)
	}
	// Lunes 2026-06-08 01:00 UTC es domingo 7 a las 19:00 en México: cae en la
	// semana del lunes 1.
	b := FlowWeekBounds(ts("2026-06-08T01:00:00Z"), ts("2026-06-09T00:00:00Z"), mx)
	if len(b) != 2 || !b[0][1].Equal(ts("2026-06-08T06:00:00Z")) {
		t.Fatalf("semanas = %v, quiero el corte en 2026-06-08T06:00Z", b)
	}
}

// CU-036-3: lead time y cycle time (mediana y p85) por semana, cerrados y
// WIP, con la confianza «aproximada» si sale del histórico importado.
func TestComputeFlowMetrics_leadCycleYWIP(t *testing.T) {
	issues := []FlowIssue{
		{ // Histórico: creado el 1, en curso el 2, hecho el 9 (semana 2).
			Number: 1, State: done, CreatedAt: ts("2026-06-01T10:00:00Z"),
			Events: []FlowEvent{
				created("2026-06-01T10:00:00Z", sync),
				move("2026-06-02T10:00:00Z", hist, todo, doing),
				move("2026-06-09T10:00:00Z", hist, doing, done),
			},
		},
		{ // En vivo: creado el 3, en curso el 5, hecho el 10 (semana 2).
			Number: 2, State: done, CreatedAt: ts("2026-06-03T10:00:00Z"),
			Events: []FlowEvent{
				created("2026-06-03T10:00:00Z", live),
				move("2026-06-05T10:00:00Z", live, todo, doing),
				move("2026-06-10T10:00:00Z", live, doing, done),
			},
		},
		{ // Hecho sin pasar por en curso: lead time sí, cycle time no.
			Number: 3, State: done, CreatedAt: ts("2026-06-08T10:00:00Z"),
			Events: []FlowEvent{
				created("2026-06-08T10:00:00Z", live),
				move("2026-06-12T10:00:00Z", live, todo, done),
			},
		},
		{ // Sigue en curso.
			Number: 4, State: doing, CreatedAt: ts("2026-06-01T10:00:00Z"),
			Events: []FlowEvent{
				created("2026-06-01T10:00:00Z", live),
				move("2026-06-04T10:00:00Z", live, todo, doing),
			},
		},
	}
	m := ComputeFlowMetrics(issues, query("2026-06-01T00:00:00Z", "2026-06-15T00:00:00Z"))
	if len(m.Weeks) != 2 {
		t.Fatalf("semanas = %d, quiero 2", len(m.Weeks))
	}
	w1, w2 := m.Weeks[0], m.Weeks[1]
	if w1.ClosedValue != 0 || w1.WIP != 3 || w1.Started != 3 {
		t.Errorf("semana 1 = %+v, quiero 0 cerrados, WIP 3 y 3 empezados", w1)
	}
	if w2.ClosedValue != 3 || w2.WIP != 1 || w2.Started != 0 {
		t.Errorf("semana 2 = %+v, quiero 3 cerrados, WIP 1 y 0 empezados", w2)
	}
	// Lead: 8 días (#1), 7 días (#2), 4 días (#3) → ordenados 4, 7, 8.
	day := 24 * time.Hour
	if w2.LeadTime != (DurationStats{Count: 3, Median: 7 * day, P85: 8 * day}) {
		t.Errorf("lead time = %+v", w2.LeadTime)
	}
	// Cycle: 7 días (#1) y 5 días (#2).
	if w2.CycleTime != (DurationStats{Count: 2, Median: 5 * day, P85: 7 * day}) {
		t.Errorf("cycle time = %+v", w2.CycleTime)
	}
	if got := w2.Source.Confidence(); got != MetricConfidenceApproximate {
		t.Errorf("confianza de la semana 2 = %v, quiero aproximada (hay histórico)", got)
	}
	if w2.Source.ImportHistory != 2 || w2.Source.Live != 3 {
		t.Errorf("fuentes de la semana 2 = %+v, quiero 2 del histórico (#1) y 3 en vivo", w2.Source)
	}
	if m.Summary.LeadTime.Count != 3 || m.Summary.ClosedValue != 3 || m.Summary.WIP != 1 || m.Summary.Started != 3 {
		t.Errorf("resumen = %+v", m.Summary)
	}
	if m.Issues != 4 {
		t.Errorf("issues = %d, quiero 4", m.Issues)
	}
}

func TestComputeFlowMetrics_soloEnVivoEsRegistrado(t *testing.T) {
	issues := []FlowIssue{{
		Number: 1, State: done, CreatedAt: ts("2026-06-01T10:00:00Z"),
		Events: []FlowEvent{created("2026-06-01T10:00:00Z", live), move("2026-06-02T10:00:00Z", live, todo, done)},
	}}
	m := ComputeFlowMetrics(issues, query("2026-06-01T00:00:00Z", "2026-06-15T00:00:00Z"))
	if got := m.Weeks[0].Source.Confidence(); got != MetricConfidenceRecorded {
		t.Errorf("confianza = %v, quiero registrada", got)
	}
	if got := m.Weeks[1].Source.Confidence(); got != MetricConfidenceNoData {
		t.Errorf("semana sin transiciones = %v, quiero sin datos", got)
	}
	if m.Weeks[1].LeadTime.Count != 0 {
		t.Errorf("semana vacía con lead time %+v", m.Weeks[1].LeadTime)
	}
}

// CU-036-4: con etiquetas de valor, los cerrados se separan; lead time es
// de todo lo hecho (decisión de Victor, 2026-11-13).
func TestComputeFlowMetrics_etiquetasDeValor(t *testing.T) {
	doneIssue := func(n int32, labels ...string) FlowIssue {
		return FlowIssue{
			Number: n, State: done, CreatedAt: ts("2026-06-01T10:00:00Z"), LabelIDs: labels,
			Events: []FlowEvent{move("2026-06-03T10:00:00Z", live, todo, done)},
		}
	}
	issues := []FlowIssue{doneIssue(1, "feature"), doneIssue(2, "bug", "area"), doneIssue(3, "area"), doneIssue(4)}

	q := query("2026-06-01T00:00:00Z", "2026-06-08T00:00:00Z")
	q.ValueLabelIDs = []string{"feature", "bug"}
	w := ComputeFlowMetrics(issues, q).Weeks[0]
	if w.ClosedValue != 2 || w.ClosedOther != 2 || w.LeadTime.Count != 4 {
		t.Errorf("con valor = %+v, quiero 2 de valor, 2 otros y lead time de los 4", w)
	}

	w = ComputeFlowMetrics(issues, query("2026-06-01T00:00:00Z", "2026-06-08T00:00:00Z")).Weeks[0]
	if w.ClosedValue != 4 || w.ClosedOther != 0 {
		t.Errorf("sin etiquetas de valor = %+v, quiero los 4 como valor", w)
	}
}

// CU-036-5: en curso hace más de 30 días sin eventos, ordenados por
// antigüedad.
func TestComputeFlowMetrics_estancados(t *testing.T) {
	inProgress := func(n int32, since string, extra ...FlowEvent) FlowIssue {
		evs := append([]FlowEvent{
			created("2026-01-05T10:00:00Z", sync),
			move(since, hist, todo, doing),
		}, extra...)
		return FlowIssue{Number: n, Title: "inventado", State: doing, CreatedAt: ts("2026-01-05T10:00:00Z"), Events: evs}
	}
	issues := []FlowIssue{
		inProgress(1, "2026-03-02T10:00:00Z"),
		inProgress(2, "2026-02-02T10:00:00Z"),
		// Comentado en vivo hace poco: no está estancado.
		inProgress(3, "2026-01-12T10:00:00Z", note("2026-05-25T10:00:00Z", live)),
		// Una edición de la sincronización no es actividad.
		inProgress(4, "2026-02-16T10:00:00Z", note("2026-05-28T10:00:00Z", sync)),
		// Entró a en curso hace menos de 30 días.
		inProgress(5, "2026-05-20T10:00:00Z"),
	}
	m := ComputeFlowMetrics(issues, query("2026-05-04T00:00:00Z", "2026-06-01T00:00:00Z"))
	var got []int32
	for _, s := range m.Stale {
		got = append(got, s.Number)
	}
	want := []int32{2, 4, 1}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("estancados = %v, quiero %v (por antigüedad en curso)", got, want)
	}
	if m.StaleTotal != 3 || m.Stale[0].Confidence != MetricConfidenceApproximate {
		t.Errorf("total %d, confianza %v", m.StaleTotal, m.Stale[0].Confidence)
	}
	if !m.Stale[1].LastActivityAt.Equal(ts("2026-02-16T10:00:00Z")) {
		t.Errorf("última actividad del #4 = %v, quiero la entrada a en curso", m.Stale[1].LastActivityAt)
	}

	q := query("2026-05-04T00:00:00Z", "2026-06-01T00:00:00Z")
	q.StaleMax = 1
	m = ComputeFlowMetrics(issues, q)
	if len(m.Stale) != 1 || m.StaleTotal != 3 || m.Stale[0].Number != 2 {
		t.Errorf("con tope 1 = %+v (total %d)", m.Stale, m.StaleTotal)
	}
}

// import_sync: no duplica lo que el histórico ya trae, y cuenta si llega
// después del histórico o si el issue no tiene histórico.
func TestComputeFlowMetrics_importSync(t *testing.T) {
	q := query("2026-06-01T00:00:00Z", "2026-06-29T00:00:00Z")

	// La sincronización registró el cierre el 20 (fecha de la corrida); el
	// histórico lo trae el 3 con su commit. Cuenta el del histórico.
	dup := FlowIssue{
		Number: 1, State: done, CreatedAt: ts("2026-06-01T10:00:00Z"),
		Events: []FlowEvent{
			move("2026-06-03T10:00:00Z", hist, todo, done),
			move("2026-06-20T10:00:00Z", sync, todo, done),
		},
	}
	m := ComputeFlowMetrics([]FlowIssue{dup}, q)
	if m.Weeks[0].ClosedValue != 1 || m.Summary.ClosedValue != 1 || m.Summary.Source.ImportSync != 0 {
		t.Errorf("cierre duplicado: semanas %+v", m.Weeks)
	}

	// Después del histórico, un cambio que solo trajo la sincronización.
	later := FlowIssue{
		Number: 2, State: done, CreatedAt: ts("2026-06-01T10:00:00Z"),
		Events: []FlowEvent{
			move("2026-06-02T10:00:00Z", hist, todo, doing),
			move("2026-06-17T10:00:00Z", sync, doing, done),
		},
	}
	m = ComputeFlowMetrics([]FlowIssue{later}, q)
	if m.Weeks[2].ClosedValue != 1 || m.Weeks[2].Source.ImportSync != 1 {
		t.Errorf("cambio posterior al histórico: semana 3 = %+v", m.Weeks[2])
	}

	// Sin histórico: la sincronización es lo único que hay.
	onlySync := FlowIssue{
		Number: 3, State: doing, CreatedAt: ts("2026-06-01T10:00:00Z"),
		Events: []FlowEvent{created("2026-06-01T10:00:00Z", sync), move("2026-06-10T10:00:00Z", sync, todo, doing)},
	}
	m = ComputeFlowMetrics([]FlowIssue{onlySync}, q)
	if m.Weeks[1].Started != 1 || m.Summary.WIP != 1 {
		t.Errorf("solo sincronización: %+v", m.Weeks[1])
	}
}

func TestComputeFlowMetrics_hechoSinFechas(t *testing.T) {
	// Importado ya hecho y sin histórico: no entra a cerrados ni a lead time.
	issues := []FlowIssue{{
		Number: 1, State: done, CreatedAt: ts("2026-06-02T10:00:00Z"),
		Events: []FlowEvent{created("2026-06-02T10:00:00Z", sync)},
	}}
	m := ComputeFlowMetrics(issues, query("2026-06-01T00:00:00Z", "2026-06-15T00:00:00Z"))
	if m.Summary.ClosedValue != 0 || m.Summary.LeadTime.Count != 0 || m.DoneWithoutDates != 1 {
		t.Errorf("hecho sin fechas: resumen %+v, sin fechas %d", m.Summary, m.DoneWithoutDates)
	}
}

func TestComputeFlowMetrics_reabiertoYDescartado(t *testing.T) {
	issues := []FlowIssue{
		{ // Hecho el 3, reabierto el 4, hecho otra vez el 10.
			Number: 1, State: done, CreatedAt: ts("2026-06-01T10:00:00Z"),
			Events: []FlowEvent{
				move("2026-06-02T10:00:00Z", live, todo, doing),
				move("2026-06-03T10:00:00Z", live, doing, done),
				move("2026-06-04T10:00:00Z", live, done, doing),
				move("2026-06-10T10:00:00Z", live, doing, done),
			},
		},
		{ // Descartado el 5.
			Number: 2, State: canceled, CreatedAt: ts("2026-06-01T10:00:00Z"),
			Events: []FlowEvent{move("2026-06-05T10:00:00Z", live, todo, canceled)},
		},
	}
	m := ComputeFlowMetrics(issues, query("2026-06-01T00:00:00Z", "2026-06-15T00:00:00Z"))
	w1, w2 := m.Weeks[0], m.Weeks[1]
	if w1.ClosedValue != 0 || w1.Canceled != 1 || w1.WIP != 1 {
		t.Errorf("semana 1 = %+v, quiero 0 cerrados (se reabrió), 1 descartado y WIP 1", w1)
	}
	if w2.ClosedValue != 1 || w2.CycleTime.Median != 8*24*time.Hour {
		t.Errorf("semana 2 = %+v, quiero 1 cerrado con cycle time desde la primera entrada a en curso", w2)
	}
}

func TestComputeFlowMetrics_issuesPosterioresATo(t *testing.T) {
	issues := []FlowIssue{{Number: 1, State: todo, CreatedAt: ts("2026-07-01T10:00:00Z")}}
	m := ComputeFlowMetrics(issues, query("2026-06-01T00:00:00Z", "2026-06-15T00:00:00Z"))
	if m.Issues != 0 {
		t.Errorf("issues = %d, quiero 0", m.Issues)
	}
}

func TestDurationStats(t *testing.T) {
	h := time.Hour
	got := durationStats([]time.Duration{5 * h, 1 * h, 3 * h, 2 * h, 4 * h, 10 * h, 6 * h, 7 * h, 8 * h, 9 * h})
	// n = 10: mediana en la posición 5, p85 en la 9.
	if got != (DurationStats{Count: 10, Median: 5 * h, P85: 9 * h}) {
		t.Errorf("stats = %+v", got)
	}
	if one := durationStats([]time.Duration{h}); one != (DurationStats{Count: 1, Median: h, P85: h}) {
		t.Errorf("uno = %+v", one)
	}
}

func TestMetricSourceConfidence(t *testing.T) {
	cases := []struct {
		s    MetricSource
		want MetricConfidence
	}{
		{MetricSource{}, MetricConfidenceNoData},
		{MetricSource{Live: 3}, MetricConfidenceRecorded},
		{MetricSource{Live: 3, ImportFile: 1}, MetricConfidenceApproximate},
		{MetricSource{ImportSync: 1}, MetricConfidenceApproximate},
	}
	for _, c := range cases {
		if got := c.s.Confidence(); got != c.want {
			t.Errorf("%+v = %v, quiero %v", c.s, got, c.want)
		}
	}
}

// Dos transiciones con la misma fecha (p. ej. un commit que pasa por dos
// estados, o el reloj): cuenta el orden del historial.
func TestComputeFlowMetrics_mismaFecha(t *testing.T) {
	issues := []FlowIssue{{
		Number: 1, State: done, CreatedAt: ts("2026-06-01T10:00:00Z"),
		Events: []FlowEvent{
			move("2026-06-02T10:00:00Z", live, todo, doing),
			move("2026-06-02T10:00:00Z", live, doing, done),
		},
	}}
	w := ComputeFlowMetrics(issues, query("2026-06-01T00:00:00Z", "2026-06-08T00:00:00Z")).Weeks[0]
	if w.ClosedValue != 1 || w.Started != 1 || w.CycleTime != (DurationStats{Count: 1}) {
		t.Errorf("semana = %+v, quiero 1 cerrado, 1 empezado y cycle time 0", w)
	}
}

// Un evento con la categoría de origen vacía (datos viejos o rotos) no deja
// la línea de tiempo sin categoría: se usa la actual.
func TestComputeFlowMetrics_origenInvalido(t *testing.T) {
	issues := []FlowIssue{{
		Number: 1, State: doing, CreatedAt: ts("2026-06-01T10:00:00Z"),
		Events: []FlowEvent{move("2026-06-02T10:00:00Z", live, "", "")},
	}}
	m := ComputeFlowMetrics(issues, query("2026-06-01T00:00:00Z", "2026-06-08T00:00:00Z"))
	if m.Summary.WIP != 1 {
		t.Errorf("WIP = %d, quiero 1 (la categoría actual)", m.Summary.WIP)
	}
}

// La entrada a en curso sostiene el cycle time y «empezados», pero suma una
// sola vez a la fuente de la semana.
func TestComputeFlowMetrics_fuenteSinDuplicar(t *testing.T) {
	issues := []FlowIssue{{
		Number: 1, State: done, CreatedAt: ts("2026-06-01T10:00:00Z"),
		Events: []FlowEvent{
			move("2026-06-02T10:00:00Z", hist, todo, doing),
			move("2026-06-03T10:00:00Z", live, doing, done),
		},
	}}
	w := ComputeFlowMetrics(issues, query("2026-06-01T00:00:00Z", "2026-06-08T00:00:00Z")).Weeks[0]
	if w.Source != (MetricSource{Live: 1, ImportHistory: 1}) || w.Started != 1 || w.CycleTime.Count != 1 {
		t.Errorf("semana = %+v, quiero una transición de cada fuente", w)
	}
}

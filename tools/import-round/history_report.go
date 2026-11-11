package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

// errHistoryNeedsContract: el histórico todavía no se puede cargar. El
// contrato (protos v0.5.0) no tiene un RPC para eventos con fecha y fuente;
// lo que haría falta está en las Notas del issue 036.
var errHistoryNeedsContract = errors.New("el histórico todavía no se puede cargar (falta el contrato: ver las Notas del issue 036); corre con -dry-run")

// runHistory reconstruye el histórico de los issues de src y lo reporta
// (issue 036, parte 1). Por ahora solo con -dry-run. La copia ya pasó por
// checkGit (salvo -skip-git-check).
func runHistory(o options, src *source, out io.Writer) error {
	if !o.dryRun {
		return errHistoryNeedsContract
	}
	docs, broken, err := src.read()
	if err != nil {
		return err
	}
	plan := MakePlan(docs, Existing{})
	rep := &HistoryReport{Key: src.key, Total: len(docs), Broken: broken, Plan: plan, Detail: o.detail}

	files := make([]string, len(plan.Import))
	for i, d := range plan.Import {
		files[i] = d.File
	}
	logs, err := folderLog(src.dir, files)
	if err != nil {
		return err
	}
	blobs, err := newBlobReader(src.dir)
	if err != nil {
		return err
	}
	defer blobs.Close()
	for _, d := range plan.Import {
		h := BuildHistory(d, readStates(blobs, d, logs[d.File]))
		h.Doc.Body = "" // del cuerpo no se usa nada más: no se guarda
		rep.Histories = append(rep.Histories, h)
	}
	rep.Write(out, true)
	return nil
}

// HistoryReport junta lo que pasó en una corrida del histórico.
type HistoryReport struct {
	Key       string
	Total     int
	Plan      Plan
	Broken    []Failure
	Failed    []Failure
	Histories []History
	// Loaded son las claves de eventos ya cargados (vacío en dry-run).
	Loaded map[string]bool
	// Detail: listar cada evento.
	Detail bool
}

// Write imprime el reporte, con el formato de import-round.
func (r *HistoryReport) Write(w io.Writer, dryRun bool) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }
	if dryRun {
		p("== import-round -historico (dry-run: no se llamó al servicio)")
	} else {
		p("== import-round -historico")
	}
	var events, status, category, fromCommit, fromFile, pending int
	var noGit, createdFile, detectedLate, closedFile, resolvedIgnored, unknown, unreadable []History
	for _, h := range r.Histories {
		events += len(h.Events)
		pending += len(h.Pending(r.Loaded))
		for _, e := range h.Events {
			if e.Kind == KindStatus {
				status++
				if e.From != e.To {
					category++
				}
			}
			if e.Source == SourceFile {
				fromFile++
			} else {
				fromCommit++
			}
		}
		add := func(list *[]History, cond bool) {
			if cond {
				*list = append(*list, h)
			}
		}
		add(&noGit, h.NoGit)
		add(&createdFile, h.CreatedFromFile)
		add(&detectedLate, h.DetectedAfterFirstCommit)
		add(&closedFile, h.ClosedFromFile)
		add(&resolvedIgnored, h.ResolvedIgnored != "")
		add(&unknown, len(h.Unknown) > 0)
		add(&unreadable, len(h.Unreadable) > 0)
	}
	p("Archivos leídos: %d · con histórico: %d · omitidos por número: %d · rotos: %d · con error: %d",
		r.Total, len(r.Histories)-len(noGit), countLosers(r.Plan.Duplicates), len(r.Broken), len(r.Failed))
	p("Eventos: %d (altas: %d · cambios de Estado: %d, %d con cambio de categoría) · de commits: %d · del archivo: %d",
		events, events-status, status, category, fromCommit, fromFile)
	p("Nuevos: %d · ya cargados: %d", pending, events-pending)
	p("Confianza: histórico aproximado (los estados se cambiaban a mano y a veces tarde)")

	section := func(title string, n int) bool {
		if n == 0 {
			return false
		}
		p("\n-- %s (%d)", title, n)
		return true
	}
	if r.Detail && section("Eventos", len(r.Histories)) {
		for _, h := range r.Histories {
			p("  %s-%d  %s", r.Key, h.Doc.Number, h.Doc.Path)
			for _, e := range h.Events {
				p("    %s", e.describe())
			}
		}
	}
	if section("Alta tomada de «Fecha detectado» (anterior al primer commit)", len(createdFile)) {
		for _, h := range createdFile {
			p("  %s-%d  %s  [%s]", r.Key, h.Doc.Number, h.Doc.Path, h.Doc.RawDate)
		}
	}
	if section("Cierre tomado de «Fecha resuelto» (ningún commit lo marca)", len(closedFile)) {
		for _, h := range closedFile {
			p("  %s-%d  %s  [%s]", r.Key, h.Doc.Number, h.Doc.Path, h.Doc.RawResolved)
		}
	}
	if section("«Fecha detectado» posterior al primer commit (la creación en Tekitla quedó con esa fecha)", len(detectedLate)) {
		for _, h := range detectedLate {
			p("  %s-%d  %s  [%s] primer commit %s", r.Key, h.Doc.Number, h.Doc.Path, h.Doc.RawDate, h.firstCommitDate())
		}
	}
	if section("«Fecha resuelto» sin usar", len(resolvedIgnored)) {
		for _, h := range resolvedIgnored {
			p("  %s-%d  %s  [%s]: %s", r.Key, h.Doc.Number, h.Doc.Path, h.Doc.RawResolved, h.ResolvedIgnored)
		}
	}
	if section("Estado desconocido en algún commit (cuenta como TODO)", len(unknown)) {
		for _, h := range unknown {
			for _, u := range h.Unknown {
				p("  %s  %s  [%s]", h.Doc.Path, short(u.Commit), valueOr(u.Value))
			}
		}
	}
	if section("Versiones que no se pudieron leer (no entran)", len(unreadable)) {
		for _, h := range unreadable {
			for _, v := range h.Unreadable {
				p("  %s  %s  %s: %s", h.Doc.Path, short(v.Commit), v.Path, v.Err)
			}
		}
	}
	if section("Sin commits (sin histórico)", len(noGit)) {
		for _, h := range noGit {
			p("  %s", h.Doc.Path)
		}
	}
	if section("Números repetidos (solo entra el que se queda con el número)", len(r.Plan.Duplicates)) {
		for _, d := range r.Plan.Duplicates {
			if d.Winner != nil {
				p("  %s-%d se queda con %s", r.Key, d.Number, d.Winner.Path)
			}
			for _, l := range d.Losers {
				p("    queda afuera: %s", l.Path)
			}
		}
	}
	if section("Con error al leer git", len(r.Failed)) {
		for _, f := range r.Failed {
			p("  %s: %s", f.Doc.Path, f.Err)
		}
	}
	if section("Archivos rotos", len(r.Broken)) {
		for _, f := range r.Broken {
			p("  %s: %s", f.Doc.Path, f.Err)
		}
	}
}

// describe es una línea del detalle: fecha, qué pasó y de dónde sale.
func (e HistoryEvent) describe() string {
	src := "archivo"
	if e.Source == SourceCommit {
		src = "commit " + short(e.Commit)
	}
	date := e.Date.Format(time.DateOnly)
	if e.Kind == KindCreated {
		return fmt.Sprintf("%s  alta  (%s)", date, src)
	}
	return fmt.Sprintf("%s  %s → %s  [%s → %s]  (%s)", date, stateName(e.From), stateName(e.To), leadingToken(e.FromRaw), leadingToken(e.ToRaw), src)
}

func (h History) firstCommitDate() string {
	for _, e := range h.Events {
		if e.Kind == KindCreated && e.Source == SourceCommit {
			return e.Date.UTC().Format(time.DateOnly)
		}
	}
	return "?"
}

func stateName(s issuesv1.IssueState) string {
	return strings.TrimPrefix(s.String(), "ISSUE_STATE_")
}

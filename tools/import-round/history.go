package main

import (
	"time"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

// Histórico de Round (issue 036, parte 1): las transiciones de estado de
// cada issue, reconstruidas desde git. Cada commit que cambia la línea
// «Estado:» es una transición con la fecha del commit. Si el archivo trae
// fechas que ningún commit marca, se usan con fuente «archivo» (CU-036-2).
//
// Todo lo que sale de acá es «histórico aproximado»: los estados se
// cambiaban a mano y a veces tarde.

// Fuente de un evento del histórico.
const (
	SourceCommit = "commit"
	SourceFile   = "archivo"
)

// Tipo de evento del histórico.
const (
	KindCreated = "alta"
	KindStatus  = "estado"
)

// Version es el estado de un archivo de issue en un commit.
type Version struct {
	Commit string
	// Date es la fecha del commit (committer).
	Date time.Time
	// Path es la ruta del archivo en ese commit, relativa a la raíz del
	// repo (cambia si el archivo se renombró).
	Path string
	// RawState es la línea «Estado:» de la cabecera en ese commit.
	RawState string
	// Err no vacío: la versión no se pudo leer (no entra al histórico).
	Err string
}

// HistoryEvent es un evento del histórico de un issue.
type HistoryEvent struct {
	// Key reconoce el evento entre corridas (idempotencia, CU-036-1):
	// <source_key>#alta, <source_key>#inicial (el estado con que nace el
	// archivo, si no es TODO) o <source_key>#<commit>. Reescribir la
	// historia de git (rebase) cambia los sha y, con ellos, las claves.
	Key  string
	Kind string
	Date time.Time
	// Source es SourceCommit o SourceFile; Commit, el sha si viene de uno.
	Source string
	Commit string
	// Solo en KindStatus: valores crudos del Estado y su categoría.
	FromRaw, ToRaw string
	From, To       issuesv1.IssueState
}

// UnknownValue es un Estado que no se reconoció en algún commit.
type UnknownValue struct {
	Commit string
	Value  string
}

// History es el histórico reconstruido de un issue.
type History struct {
	Doc    *Doc
	Events []HistoryEvent
	// NoGit: el archivo no tiene commits legibles (no hay histórico).
	NoGit bool
	// CreatedFromFile: el alta sale de «Fecha detectado», anterior al
	// primer commit.
	CreatedFromFile bool
	// DetectedAfterFirstCommit: «Fecha detectado» es posterior al primer
	// commit. El alta del histórico es el commit, pero ImportIssue ya dejó
	// el issue con «Fecha detectado» como fecha de creación.
	DetectedAfterFirstCommit bool
	// ClosedFromFile: el cierre sale de «Fecha resuelto» (CU-036-2).
	ClosedFromFile bool
	// ResolvedIgnored: por qué «Fecha resuelto» no se usó ("" si se usó, si
	// no hay o si un commit marca el cierre).
	ResolvedIgnored string
	Unknown         []UnknownValue
	Unreadable      []Version
}

// BuildHistory arma el histórico de cur (la versión actual del archivo) a
// partir de sus versiones en git, de la más vieja a la más nueva.
//
//   - Alta: «Fecha detectado» si es anterior al primer commit; si no, el
//     primer commit.
//   - Si el archivo nace en un estado que no es de la categoría TODO, hay
//     una transición desde «open» (el alta). Si nace cerrado y tiene
//     «Fecha resuelto» (entre el alta y el primer commit), esa es la fecha
//     del cierre, con fuente archivo: ningún commit lo marca. Si no, la
//     fecha del primer commit con línea Estado.
//   - Después, cada commit en que cambia el valor del Estado (el primer
//     token, como MapState) es una transición. Un commit que solo cambia el
//     texto alrededor no lo es; un commit sin línea Estado se salta.
func BuildHistory(cur *Doc, versions []Version) History {
	h := History{Doc: cur}
	var readable []Version
	for _, v := range versions {
		if v.Err != "" {
			h.Unreadable = append(h.Unreadable, v)
			continue
		}
		readable = append(readable, v)
	}
	if len(readable) == 0 {
		h.NoGit = true
		return h
	}
	key := cur.SourceKey
	first := readable[0]

	created := HistoryEvent{Key: key + "#alta", Kind: KindCreated, Date: first.Date, Source: SourceCommit, Commit: first.Commit}
	if det, ok := ParseDate(cur.RawDate); ok {
		if dayBefore(det, first.Date) {
			created.Date, created.Source, created.Commit = det, SourceFile, ""
			h.CreatedFromFile = true
		} else if dayAfter(det, first.Date) {
			h.DetectedAfterFirstCommit = true
		}
	}
	h.Events = append(h.Events, created)

	resolved, hasResolved := ParseDate(cur.RawResolved)
	usedResolved := false

	prevTok, prevRaw := "", ""
	seenUnknown := map[string]bool{}
	for _, v := range readable {
		tok := leadingToken(v.RawState)
		if tok == "" {
			continue
		}
		to, known := MapState(v.RawState)
		if !known && !seenUnknown[tok] {
			// Una vez por valor, en el primer commit que lo trae.
			seenUnknown[tok] = true
			h.Unknown = append(h.Unknown, UnknownValue{Commit: v.Commit, Value: v.RawState})
		}
		if prevTok == "" {
			// Primer valor del Estado: desde el alta, como «open».
			prevTok, prevRaw = tok, v.RawState
			if to == issuesv1.IssueState_ISSUE_STATE_TODO {
				continue
			}
			// La clave no depende de la fuente: si después se agrega o se
			// corrige «Fecha resuelto», el evento sigue siendo el mismo.
			ev := HistoryEvent{Key: key + "#inicial", Kind: KindStatus, Date: v.Date, Source: SourceCommit, Commit: v.Commit,
				FromRaw: "open", ToRaw: v.RawState, From: issuesv1.IssueState_ISSUE_STATE_TODO, To: to}
			if isClosed(to) && hasResolved {
				switch {
				case dayBefore(resolved, created.Date):
					h.ResolvedIgnored = "anterior al alta"
				case dayAfter(resolved, v.Date):
					h.ResolvedIgnored = "posterior al primer commit con Estado (que ya lo trae cerrado)"
				default:
					ev.Date, ev.Source, ev.Commit = resolved, SourceFile, ""
					// Mismo día que el alta: la fecha del archivo es medianoche y
					// el alta puede tener hora. El cierre no va antes del alta.
					if ev.Date.Before(created.Date) {
						ev.Date = created.Date
					}
					h.ClosedFromFile, usedResolved = true, true
				}
			}
			h.Events = append(h.Events, ev)
			continue
		}
		if tok == prevTok {
			continue
		}
		from, _ := MapState(prevRaw)
		h.Events = append(h.Events, HistoryEvent{Key: key + "#" + v.Commit, Kind: KindStatus, Date: v.Date, Source: SourceCommit, Commit: v.Commit,
			FromRaw: prevRaw, ToRaw: v.RawState, From: from, To: to})
		prevTok, prevRaw = tok, v.RawState
	}

	if hasResolved && !usedResolved && h.ResolvedIgnored == "" {
		final, _ := MapState(prevRaw)
		if !isClosed(final) {
			h.ResolvedIgnored = "el issue no está cerrado"
		}
	}
	return h
}

// dayBefore y dayAfter comparan por día: las fechas del archivo no tienen
// hora, y una del mismo día que el commit no es ni anterior ni posterior.
// Cada fecha cuenta en su zona (la del commit es la de quien lo hizo).
func dayBefore(d, ref time.Time) bool {
	return d.Format(time.DateOnly) < ref.Format(time.DateOnly)
}

func dayAfter(d, ref time.Time) bool {
	return d.Format(time.DateOnly) > ref.Format(time.DateOnly)
}

func isClosed(s issuesv1.IssueState) bool {
	return s == issuesv1.IssueState_ISSUE_STATE_DONE || s == issuesv1.IssueState_ISSUE_STATE_CANCELED
}

// Pending son los eventos de h que no están en loaded (claves ya cargadas):
// con lo que ya se cargó, una segunda corrida no agrega nada (CU-036-1).
func (h History) Pending(loaded map[string]bool) []HistoryEvent {
	var out []HistoryEvent
	for _, e := range h.Events {
		if !loaded[e.Key] {
			out = append(out, e)
		}
	}
	return out
}

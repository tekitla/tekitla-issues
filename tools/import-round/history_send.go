package main

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

// Carga del histórico (issue 036, protos v0.6.0): por cada issue ya
// importado, corrige su alta si hace falta (ImportIssue con
// override_created_at) y manda sus transiciones con ImportIssueEvents.
// Idempotente: el servicio reconoce cada evento por su source_ref.

// importEventsBatch es el máximo de eventos por llamada (el del contrato).
const importEventsBatch = 500

// target es el proyecto destino y sus issues, leídos una vez.
type target struct {
	project     *issuesv1.Project
	bySourceKey map[string]*issuesv1.Issue
	issues      []*issuesv1.Issue
}

// loadTarget busca el proyecto (tiene que existir: el histórico va sobre
// issues ya importados) y lee sus issues.
func loadTarget(ctx context.Context, client issuesv1.IssueServiceClient, src *source) (*target, error) {
	project, err := findProject(ctx, client, src, false)
	if err != nil {
		return nil, err
	}
	issues, err := listIssues(ctx, client, project.GetId())
	if err != nil {
		return nil, err
	}
	t := &target{project: project, issues: issues, bySourceKey: map[string]*issuesv1.Issue{}}
	for _, it := range issues {
		if k := it.GetSourceKey(); k != "" {
			t.bySourceKey[k] = it
		}
	}
	return t, nil
}

// existing es lo que ya hay, para MakePlan.
func (t *target) existing() Existing {
	out := Existing{}
	for _, it := range t.issues {
		out[it.GetNumber()] = it.GetSourceKey()
	}
	return out
}

// SendResult es lo que pasó al cargar el histórico.
type SendResult struct {
	Created, Skipped, Updated int
	// CreatedFixed: issues cuya alta se corrigió con override_created_at.
	CreatedFixed []*Doc
	// NotImported: issues sin importar en Tekitla (no se carga su histórico).
	NotImported []*Doc
	Failed      []Failure
}

// sendHistories carga el histórico de cada issue. Un error en un issue no
// frena a los demás: va al reporte.
func sendHistories(ctx context.Context, client issuesv1.IssueServiceClient, src *source, t *target, hs []History, overwrite bool) *SendResult {
	res := &SendResult{}
	fail := func(d *Doc, call string, err error) {
		res.Failed = append(res.Failed, Failure{Doc: d, Err: call + ": " + status.Convert(err).Message() + " (" + status.Code(err).String() + ")"})
	}
	for _, h := range hs {
		if h.NoGit || len(h.Events) == 0 {
			continue
		}
		it := t.bySourceKey[h.Doc.SourceKey]
		if it == nil {
			res.NotImported = append(res.NotImported, h.Doc)
			continue
		}
		// El alta del histórico (Fecha detectado o el primer commit) manda:
		// los eventos no pueden quedar antes del alta.
		created := h.Events[0]
		if !it.GetCreatedAt().AsTime().Equal(created.Date.UTC().Truncate(time.Microsecond)) {
			req := (&Report{}).inspect(h.Doc, src.translate(h.Doc))
			req.ProjectId = t.project.GetId()
			req.CreatedAt = timestamppb.New(created.Date)
			req.OverrideCreatedAt = true
			callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err := client.ImportIssue(callCtx, req)
			cancel()
			if err != nil {
				fail(h.Doc, "ImportIssue (alta)", err)
				continue
			}
			res.CreatedFixed = append(res.CreatedFixed, h.Doc)
		}
		events := importedEvents(h)
		for start := 0; start < len(events); start += importEventsBatch {
			end := min(start+importEventsBatch, len(events))
			callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			out, err := client.ImportIssueEvents(callCtx, &issuesv1.ImportIssueEventsRequest{
				ProjectId: t.project.GetId(), SourceKey: h.Doc.SourceKey, Events: events[start:end], Overwrite: overwrite,
			})
			cancel()
			if err != nil {
				fail(h.Doc, "ImportIssueEvents", err)
				break
			}
			res.Created += int(out.GetCreated())
			res.Skipped += int(out.GetSkipped())
			res.Updated += int(out.GetUpdated())
		}
	}
	return res
}

// importedEvents traduce las transiciones de h (sin el alta) al contrato.
// La source_ref es la clave del evento sin el prefijo del issue: el sha del
// commit o "inicial".
func importedEvents(h History) []*issuesv1.ImportedEvent {
	var out []*issuesv1.ImportedEvent
	for _, e := range h.Events {
		if e.Kind != KindStatus {
			continue
		}
		src := issuesv1.EventSource_EVENT_SOURCE_IMPORT_HISTORY
		if e.Source == SourceFile {
			src = issuesv1.EventSource_EVENT_SOURCE_IMPORT_FILE
		}
		out = append(out, &issuesv1.ImportedEvent{
			SourceRef:  strings.TrimPrefix(e.Key, h.Doc.SourceKey+"#"),
			Source:     src,
			OccurredAt: timestamppb.New(e.Date),
			Change: &issuesv1.ImportedEvent_StatusChanged{StatusChanged: &issuesv1.ImportedStatusChange{
				FromState: e.From, ToState: e.To, FromName: stateValue(e.FromRaw), ToName: stateValue(e.ToRaw),
			}},
		})
	}
	return out
}

// stateValue es el valor del Estado que va como nombre en el evento: el
// primer token (como MapState), a lo sumo 40 runas como el nombre de un
// estado.
func stateValue(raw string) string {
	v := leadingToken(raw)
	if v == "" {
		v = "?"
	}
	if utf8.RuneCountInString(v) > 40 {
		v = string([]rune(v)[:40])
	}
	return v
}

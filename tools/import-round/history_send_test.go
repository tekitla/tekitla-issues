package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

// historyFake es el servicio para la carga del histórico: issues ya
// importados, ImportIssue (solo el alta) e ImportIssueEvents idempotente
// por source_ref.
type historyFake struct {
	issuesv1.IssueServiceClient // los demás RPCs no se usan (nil: panic)

	issues    map[string]*issuesv1.Issue // por clave de origen
	events    map[string][]*issuesv1.ImportedEvent
	overrides []string
	overwrite bool
}

func (f *historyFake) ListProjects(context.Context, *issuesv1.ListProjectsRequest, ...grpc.CallOption) (*issuesv1.ListProjectsResponse, error) {
	return &issuesv1.ListProjectsResponse{Projects: []*issuesv1.Project{{Id: "p1", Key: "RND"}}, Page: &issuesv1.PageResponse{}}, nil
}

func (f *historyFake) ListIssues(context.Context, *issuesv1.ListIssuesRequest, ...grpc.CallOption) (*issuesv1.ListIssuesResponse, error) {
	var out []*issuesv1.Issue
	for _, it := range f.issues {
		out = append(out, it)
	}
	return &issuesv1.ListIssuesResponse{Issues: out, Page: &issuesv1.PageResponse{}}, nil
}

func (f *historyFake) ImportIssue(_ context.Context, req *issuesv1.ImportIssueRequest, _ ...grpc.CallOption) (*issuesv1.ImportIssueResponse, error) {
	it := f.issues[req.GetSourceKey()]
	if it == nil || !req.GetOverrideCreatedAt() || req.GetProjectId() != "p1" || req.GetNumber() != it.GetNumber() {
		return nil, status.Error(codes.FailedPrecondition, "solo corrección del alta")
	}
	// Como el servicio: el alta no puede quedar después de un evento importado.
	for _, e := range f.events[req.GetSourceKey()] {
		if e.GetOccurredAt().AsTime().Before(req.GetCreatedAt().AsTime()) {
			return nil, status.Error(codes.FailedPrecondition, "IMPORT_EVENTS_BEFORE_CREATED")
		}
	}
	f.overrides = append(f.overrides, req.GetSourceKey())
	it.CreatedAt = req.GetCreatedAt()
	return &issuesv1.ImportIssueResponse{Issue: it, Outcome: issuesv1.ImportOutcome_IMPORT_OUTCOME_UPDATED}, nil
}

func (f *historyFake) ImportIssueEvents(_ context.Context, req *issuesv1.ImportIssueEventsRequest, _ ...grpc.CallOption) (*issuesv1.ImportIssueEventsResponse, error) {
	it := f.issues[req.GetSourceKey()]
	if it == nil {
		return nil, status.Error(codes.NotFound, "issue not found")
	}
	f.overwrite = f.overwrite || req.GetOverwrite()
	res := &issuesv1.ImportIssueEventsResponse{}
	for _, e := range req.GetEvents() {
		if e.GetOccurredAt().AsTime().Before(it.GetCreatedAt().AsTime()) {
			return nil, status.Error(codes.InvalidArgument, "occurred_at is before the issue's created_at")
		}
		dup := false
		for _, have := range f.events[req.GetSourceKey()] {
			dup = dup || have.GetSourceRef() == e.GetSourceRef()
		}
		if dup && req.GetOverwrite() {
			for i, have := range f.events[req.GetSourceKey()] {
				if have.GetSourceRef() == e.GetSourceRef() {
					f.events[req.GetSourceKey()][i] = e
				}
			}
			res.Updated++
			continue
		}
		if dup {
			res.Skipped++
			continue
		}
		f.events[req.GetSourceKey()] = append(f.events[req.GetSourceKey()], e)
		res.Created++
	}
	return res, nil
}

// Carga de punta a punta: corrige el alta que hace falta, manda las
// transiciones con su fuente y una segunda corrida no agrega nada
// (CU-036-1); un issue sin importar va al reporte.
func TestRunHistory_Send(t *testing.T) {
	r := newTestRepo(t)
	r.commit("2026-06-18T10:00:00Z", map[string]string{
		"001-a.md": issueMD("A", "`open`", "2026-06-01", "", "x"),
		"002-b.md": issueMD("B", "`resolved`", "2026-06-25", "2026-06-10", "x"),
		"003-c.md": issueMD("C", "`open`", "2026-06-01", "", "x"),
	})
	c2 := r.commit("2026-07-01T10:00:00Z", map[string]string{"001-a.md": issueMD("A", "`in-progress`", "2026-06-01", "", "x")})
	fake := &historyFake{
		issues: map[string]*issuesv1.Issue{
			// 001: el alta ya es «Fecha detectado». 002: «Fecha detectado» es
			// posterior al primer commit, así que el alta pasa al commit.
			"001-a": {Number: 1, SourceKey: "001-a", CreatedAt: timestamppb.New(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))},
			"002-b": {Number: 2, SourceKey: "002-b", CreatedAt: timestamppb.New(time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC))},
		},
		events: map[string][]*issuesv1.ImportedEvent{},
	}
	o := options{dir: r.issues(), history: true, user: "u1", skipGitCheck: true, client: fake}
	var out strings.Builder
	if err := run(context.Background(), o, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"== import-round -historico\n",
		"Cargados: 2 · ya estaban: 0 · reescritos: 0 · altas corregidas: 1 · sin importar en Tekitla: 1 · con error: 0",
		"RND-3  ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en:\n%s", want, got)
		}
	}
	if len(fake.overrides) != 1 || fake.overrides[0] != "002-b" || !fake.issues["002-b"].GetCreatedAt().AsTime().Equal(time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("overrides %v, alta de 002 %v", fake.overrides, fake.issues["002-b"].GetCreatedAt().AsTime())
	}
	a := fake.events["001-a"]
	if len(a) != 1 || a[0].GetSourceRef() != c2 || a[0].GetSource() != issuesv1.EventSource_EVENT_SOURCE_IMPORT_HISTORY ||
		a[0].GetStatusChanged().GetFromName() != "open" || a[0].GetStatusChanged().GetToName() != "in-progress" ||
		a[0].GetStatusChanged().GetToState() != issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS {
		t.Errorf("eventos de 001: %v", a)
	}
	// 002 nace cerrado y su «Fecha resuelto» es anterior al alta: no se usa;
	// el cierre va con el commit y la referencia «inicial».
	b := fake.events["002-b"]
	if len(b) != 1 || b[0].GetSourceRef() != "inicial" || b[0].GetSource() != issuesv1.EventSource_EVENT_SOURCE_IMPORT_HISTORY {
		t.Errorf("eventos de 002: %v", b)
	}

	out.Reset()
	o.overwrite = true
	if err := run(context.Background(), o, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Cargados: 0 · ya estaban: 0 · reescritos: 2 · altas corregidas: 0") || len(fake.overrides) != 1 || !fake.overwrite {
		t.Errorf("segunda corrida:\n%s\noverrides %v overwrite %v", out.String(), fake.overrides, fake.overwrite)
	}
}

// -sobrescribir corrige una carga anterior con eventos antes del alta
// correcta: la corrección del alta falla, se reescriben los eventos y se
// vuelve a intentar.
func TestRunHistory_OverwriteFixesEarlierLoad(t *testing.T) {
	r := newTestRepo(t)
	r.commit("2026-06-18T10:00:00Z", map[string]string{"001-a.md": issueMD("A", "`open`", "2026-06-01", "", "x")})
	c2 := r.commit("2026-07-01T10:00:00Z", map[string]string{"001-a.md": issueMD("A", "`in-progress`", "2026-06-01", "", "x")})
	wrong := &issuesv1.ImportedEvent{SourceRef: c2, Source: issuesv1.EventSource_EVENT_SOURCE_IMPORT_HISTORY,
		OccurredAt: timestamppb.New(time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC))}
	fake := &historyFake{
		issues: map[string]*issuesv1.Issue{
			"001-a": {Number: 1, SourceKey: "001-a", CreatedAt: timestamppb.New(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))},
		},
		events: map[string][]*issuesv1.ImportedEvent{"001-a": {wrong}},
	}
	o := options{dir: r.issues(), history: true, user: "u1", skipGitCheck: true, client: fake}
	var out strings.Builder
	// Sin -sobrescribir: el alta no se puede corregir y va al reporte.
	if err := run(context.Background(), o, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "con error: 1") || len(fake.overrides) != 0 {
		t.Fatalf("sin -sobrescribir:\n%s", out.String())
	}
	out.Reset()
	o.overwrite = true
	if err := run(context.Background(), o, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reescritos: 1 · altas corregidas: 1") || !strings.Contains(out.String(), "con error: 0") {
		t.Errorf("con -sobrescribir:\n%s", out.String())
	}
	if got := fake.events["001-a"][0].GetOccurredAt().AsTime(); !got.Equal(time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)) ||
		!fake.issues["001-a"].GetCreatedAt().AsTime().Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("evento %v, alta %v", got, fake.issues["001-a"].GetCreatedAt().AsTime())
	}
}

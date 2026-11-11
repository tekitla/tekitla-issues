package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

// Fixtures inventados (sin datos reales): un repo de docs con template-docs.
var fixtureIssues = map[string]string{
	// CU-035-2: abierto + alta; area repite uno de los repos.
	"030-lectura-de-ejemplo/issue.md": `---
id: 30
titulo: "Lectura de ejemplo: listar y obtener"
estado: abierto
tipo: feature
prioridad: alta
area: api-demo
repos: [api-demo, cli-demo]
creado: 2026-01-10
resuelto: null
relacionados: [2, 3]
---

# Lectura de ejemplo: listar y obtener

## Contexto

Texto del issue.
`,
	// CU-035-3: bloqueado -> En curso + etiqueta bloqueado.
	"002-algo-bloqueado/issue.md": `---
id: 2
titulo: Algo bloqueado
estado: bloqueado
tipo: deuda
prioridad: media
area: null
repos: []
creado: 2026-01-02
resuelto: null
---

# Algo bloqueado

Espera a otro.
`,
	// resuelto con fecha y plan.md.
	"003-ya-resuelto/issue.md": `---
id: 3
titulo: Ya resuelto
estado: resuelto
tipo: investigacion
prioridad: baja
area: docs-demo
repos: [docs-demo]
creado: 2026-01-03
resuelto: 2026-02-01
---

# Ya resuelto
`,
	"003-ya-resuelto/plan.md": "# Plan\n",
	"004-descartado/issue.md": `---
id: 4
titulo: Descartado
estado: descartado
tipo: feature
prioridad: media
area: null
repos: [web-demo]
creado: 2026-01-04
resuelto: null
---
`,
	"005-en-curso/issue.md": "---\nid: 5\ntitulo: En curso\nestado: en-curso\ntipo: feature\nprioridad: media\narea: api-demo\nrepos: [api-demo]\ncreado: 2026-01-05\nresuelto: null\n---\n\n# En curso\n",
	// CU-035-6: sin frontmatter y con estado desconocido.
	"006-sin-frontmatter/issue.md": "# Sin frontmatter\n\nTexto.\n",
	"007-estado-raro/issue.md":     "---\nid: 7\ntitulo: Estado raro\nestado: archivado\ntipo: feature\nprioridad: urgentisima\narea: null\nrepos: []\ncreado: 2026-01-07\nresuelto: null\n---\n",
	// No son issues: se ignoran.
	"index.md":         "# Índice\n",
	"plantilla/x.md":   "x",
	"adr-0001/plan.md": "x",
}

func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, IssuesSubdir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Carpeta de issue sin issue.md: rota.
	if err := os.MkdirAll(filepath.Join(root, IssuesSubdir, "008-sin-archivo"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// fakeIssues es un IssueService en memoria con lo que usa el importador.
// ImportIssue responde UNCHANGED si el request es igual al guardado (sin
// created_at, que solo cuenta al crear), como el servicio real.
type fakeIssues struct {
	issuesv1.IssueServiceClient // los demás RPCs no se usan (nil: panic)

	projects []*issuesv1.Project
	// issues: proyecto -> clave de origen -> último request.
	issues map[string]map[string]*issuesv1.ImportIssueRequest
}

func newFake() *fakeIssues {
	return &fakeIssues{issues: map[string]map[string]*issuesv1.ImportIssueRequest{}}
}

func (f *fakeIssues) ListProjects(context.Context, *issuesv1.ListProjectsRequest, ...grpc.CallOption) (*issuesv1.ListProjectsResponse, error) {
	return &issuesv1.ListProjectsResponse{Projects: f.projects, Page: &issuesv1.PageResponse{}}, nil
}

func (f *fakeIssues) CreateProject(_ context.Context, req *issuesv1.CreateProjectRequest, _ ...grpc.CallOption) (*issuesv1.CreateProjectResponse, error) {
	p := &issuesv1.Project{Id: "p-" + req.GetKey(), Key: req.GetKey(), Name: req.GetName()}
	f.projects = append(f.projects, p)
	f.issues[p.Id] = map[string]*issuesv1.ImportIssueRequest{}
	return &issuesv1.CreateProjectResponse{Project: p}, nil
}

func (f *fakeIssues) ListIssues(_ context.Context, req *issuesv1.ListIssuesRequest, _ ...grpc.CallOption) (*issuesv1.ListIssuesResponse, error) {
	var out []*issuesv1.Issue
	for key, r := range f.issues[req.GetProjectId()] {
		out = append(out, &issuesv1.Issue{Number: r.GetNumber(), SourceKey: key})
	}
	return &issuesv1.ListIssuesResponse{Issues: out, Page: &issuesv1.PageResponse{}}, nil
}

func (f *fakeIssues) ImportIssue(_ context.Context, req *issuesv1.ImportIssueRequest, _ ...grpc.CallOption) (*issuesv1.ImportIssueResponse, error) {
	byKey := f.issues[req.GetProjectId()]
	stored := proto.Clone(req).(*issuesv1.ImportIssueRequest)
	stored.CreatedAt = nil
	prev, ok := byKey[req.GetSourceKey()]
	if !ok {
		for _, r := range byKey {
			if r.GetNumber() == req.GetNumber() {
				return nil, status.Error(codes.AlreadyExists, "ISSUE_NUMBER_TAKEN")
			}
		}
		byKey[req.GetSourceKey()] = stored
		return &issuesv1.ImportIssueResponse{Outcome: issuesv1.ImportOutcome_IMPORT_OUTCOME_CREATED}, nil
	}
	if proto.Equal(prev, stored) {
		return &issuesv1.ImportIssueResponse{Outcome: issuesv1.ImportOutcome_IMPORT_OUTCOME_UNCHANGED}, nil
	}
	byKey[req.GetSourceKey()] = stored
	return &issuesv1.ImportIssueResponse{Outcome: issuesv1.ImportOutcome_IMPORT_OUTCOME_UPDATED}, nil
}

func (f *fakeIssues) project(key string) *issuesv1.Project {
	for _, p := range f.projects {
		if p.GetKey() == key {
			return p
		}
	}
	return nil
}

func runTemplate(t *testing.T, client *fakeIssues, root, repo, project string) string {
	t.Helper()
	var out strings.Builder
	o := options{format: formatTemplateDocs, dir: root, repo: repo, projectKey: project, user: "u1",
		createProject: true, skipGitCheck: true, client: client}
	if err := run(context.Background(), o, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	return out.String()
}

// CU-035-1, CU-035-2, CU-035-3, CU-035-4 y CU-035-6 de punta a punta.
func TestRunTemplateDocs(t *testing.T) {
	root := writeFixture(t, fixtureIssues)
	fake := newFake()

	// CU-035-1: sin -project, tekitla-docs va a TK, un issue por carpeta con
	// el número de su id.
	out := runTemplate(t, fake, root, "tekitla/tekitla-docs", "")
	p := fake.project("TK")
	if p == nil || p.GetName() != "Tekitla" || len(fake.projects) != 1 {
		t.Fatalf("projects: %+v\n%s", fake.projects, out)
	}
	got := fake.issues[p.GetId()]
	wantKeys := []string{"002-algo-bloqueado", "003-ya-resuelto", "004-descartado", "005-en-curso", "007-estado-raro", "030-lectura-de-ejemplo"}
	if len(got) != len(wantKeys) {
		t.Fatalf("imported %d issues, want %d\n%s", len(got), len(wantKeys), out)
	}
	for _, k := range wantKeys {
		r := got[k]
		if r == nil {
			t.Fatalf("missing %s\n%s", k, out)
		}
		if n, _, _ := ParseName(k + ".md"); r.GetNumber() != n {
			t.Errorf("%s: number %d", k, r.GetNumber())
		}
	}
	if !strings.Contains(out, "Importados: 6 · actualizados: 0") {
		t.Errorf("first run report:\n%s", out)
	}

	// CU-035-2: abierto + alta -> Por hacer, HIGH, etiquetas tipo + area +
	// repos sin repetir.
	r := got["030-lectura-de-ejemplo"]
	if r.GetState() != issuesv1.IssueState_ISSUE_STATE_TODO || r.GetPriority() != issuesv1.Priority_PRIORITY_HIGH ||
		!reflect.DeepEqual(r.GetLabelNames(), []string{"feature", "api-demo", "cli-demo"}) ||
		r.GetTitle() != "Lectura de ejemplo: listar y obtener" {
		t.Errorf("030: %v", r)
	}
	if !strings.HasPrefix(r.GetDescription(), "## Contexto") || strings.Contains(r.GetDescription(), "# Lectura") ||
		!strings.Contains(r.GetDescription(), "(https://github.com/tekitla/tekitla-docs/blob/main/contenido/issues/030-lectura-de-ejemplo/issue.md)") ||
		strings.Contains(r.GetDescription(), "plan.md") {
		t.Errorf("030 description: %q", r.GetDescription())
	}

	// CU-035-3: bloqueado -> En curso con la etiqueta bloqueado.
	r = got["002-algo-bloqueado"]
	if r.GetState() != issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS || !reflect.DeepEqual(r.GetLabelNames(), []string{"deuda", "bloqueado"}) {
		t.Errorf("002: %v", r)
	}

	// Resuelto (fecha y plan en la descripción), descartado y en curso.
	r = got["003-ya-resuelto"]
	if r.GetState() != issuesv1.IssueState_ISSUE_STATE_DONE || r.GetPriority() != issuesv1.Priority_PRIORITY_LOW ||
		!strings.Contains(r.GetDescription(), "/003-ya-resuelto/plan.md)") || !strings.Contains(r.GetDescription(), "Resuelto: 2026-02-01.") {
		t.Errorf("003: %v", r)
	}
	if r := got["004-descartado"]; r.GetState() != issuesv1.IssueState_ISSUE_STATE_CANCELED || r.GetTitle() != "Descartado" {
		t.Errorf("004: %v", r)
	}
	if r := got["005-en-curso"]; r.GetState() != issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS || r.GetPriority() != issuesv1.Priority_PRIORITY_MEDIUM {
		t.Errorf("005: %v", r)
	}

	// CU-035-6: sin frontmatter, sin issue.md y estado/prioridad
	// desconocidos van al reporte; el resto se importó.
	for _, want := range []string{
		"-- Archivos rotos (2)",
		filepath.Join("006-sin-frontmatter", "issue.md") + ": sin frontmatter",
		filepath.Join("008-sin-archivo", "issue.md") + ": la carpeta no tiene issue.md",
		"-- Estado desconocido o sin estado (van como TODO) (1)",
		filepath.Join("007-estado-raro", "issue.md") + "  [archivado]",
		"-- Prioridad desconocida o sin prioridad (van como MEDIUM) (1)",
		"[urgentisima]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q:\n%s", want, out)
		}
	}
	if r := got["007-estado-raro"]; r.GetState() != issuesv1.IssueState_ISSUE_STATE_TODO || r.GetPriority() != issuesv1.Priority_PRIORITY_MEDIUM {
		t.Errorf("007: %v", r)
	}

	// CU-035-4: de nuevo sin cambios -> 0 importados y 0 actualizados.
	out = runTemplate(t, fake, root, "tekitla/tekitla-docs", "")
	if !strings.Contains(out, "Importados: 0 · actualizados: 0 · sin cambios: 6 · con error: 0") || len(fake.projects) != 1 {
		t.Errorf("second run:\n%s", out)
	}

	// El repo de docs manda: un cambio en el archivo actualiza ese issue.
	path := filepath.Join(root, IssuesSubdir, "005-en-curso", "issue.md")
	if err := os.WriteFile(path, []byte(strings.Replace(fixtureIssues["005-en-curso/issue.md"], "estado: en-curso", "estado: resuelto", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out = runTemplate(t, fake, root, "tekitla/tekitla-docs", "")
	if !strings.Contains(out, "Importados: 0 · actualizados: 1 · sin cambios: 5") || got["005-en-curso"].GetState() != issuesv1.IssueState_ISSUE_STATE_DONE {
		t.Errorf("after edit:\n%s", out)
	}
}

// CU-035-5: el docs de vrojas1015 con -project XYZ importa en XYZ, no en VR.
func TestRunTemplateDocs_ProjectFlag(t *testing.T) {
	root := writeFixture(t, map[string]string{"005-en-curso/issue.md": fixtureIssues["005-en-curso/issue.md"]})
	fake := newFake()
	out := runTemplate(t, fake, root, "vrojas1015/docs", "xyz")
	if fake.project("XYZ") == nil || fake.project("VR") != nil || len(fake.issues["p-XYZ"]) != 1 {
		t.Errorf("projects %+v\n%s", fake.projects, out)
	}
	if !strings.Contains(out, "XYZ-5") {
		t.Errorf("report:\n%s", out)
	}
	r := fake.issues["p-XYZ"]["005-en-curso"]
	if !strings.Contains(r.GetDescription(), "https://github.com/vrojas1015/docs/blob/main/contenido/issues/005-en-curso/issue.md") {
		t.Errorf("description: %q", r.GetDescription())
	}

	// Sin -project, el docs de vrojas1015 va a VR.
	fake = newFake()
	runTemplate(t, fake, root, "vrojas1015/docs", "")
	if p := fake.project("VR"); p == nil || p.GetName() != "vrojas1015" {
		t.Errorf("default VR: %+v", fake.projects)
	}
}

func TestRunTemplateDocs_NoProject(t *testing.T) {
	root := writeFixture(t, map[string]string{})
	o := options{format: formatTemplateDocs, dir: root, repo: "tekitla/tekitla-docs", user: "u1", skipGitCheck: true, client: newFake()}
	err := run(context.Background(), o, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "no existe el proyecto TK") {
		t.Errorf("without -create-project: %v", err)
	}
}

func TestResolveSource(t *testing.T) {
	cases := []struct {
		o       options
		key     string
		wantErr string
	}{
		{options{}, "RND", ""},
		{options{format: formatRound, projectKey: "otro"}, "OTRO", ""},
		{options{format: formatTemplateDocs, dir: "x", repo: "tekitla/tekitla-docs"}, "TK", ""},
		{options{format: formatTemplateDocs, dir: "x", repo: "Vrojas1015/Docs"}, "VR", ""},
		{options{format: formatTemplateDocs, dir: "x", repo: "vrojas1015/docs", projectKey: " xyz "}, "XYZ", ""},
		{options{format: formatTemplateDocs, dir: "x", repo: "otra/cosa", projectKey: "DD"}, "DD", ""},
		{options{format: formatTemplateDocs, dir: "x", repo: "otra/cosa"}, "", "no tiene clave sugerida"},
		{options{format: formatTemplateDocs, repo: "tekitla/tekitla-docs"}, "", "falta -dir"},
		{options{format: formatTemplateDocs, dir: t.TempDir()}, "", "usa -repo"},
		{options{format: "jira"}, "", "formato desconocido"},
	}
	for _, c := range cases {
		src, err := resolveSource(c.o)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%+v: err %v, want %q", c.o, err, c.wantErr)
			}
			continue
		}
		if err != nil || src.key != c.key {
			t.Errorf("%+v: key %v err %v, want %s", c.o, src, err, c.key)
		}
	}
	src, _ := resolveSource(options{format: formatTemplateDocs, dir: "repo", repo: "tekitla/tekitla-docs"})
	if src.dir != filepath.Join("repo", "contenido", "issues") {
		t.Errorf("dir %s", src.dir)
	}
}

func TestRepoFromRemote(t *testing.T) {
	cases := map[string]string{
		"https://github.com/tekitla/tekitla-docs.git":     "tekitla/tekitla-docs",
		"https://github.com/vrojas1015/docs":              "vrojas1015/docs",
		"git@github.com:vrojas1015/docs.git\n":            "vrojas1015/docs",
		"ssh://git@github.com/tekitla/tekitla-docs/":      "tekitla/tekitla-docs",
		"https://gitlab.com/roel-education/documentacion": "",
		"": "",
	}
	for url, want := range cases {
		if got := RepoFromRemote(url); got != want {
			t.Errorf("RepoFromRemote(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestParseTemplateDoc(t *testing.T) {
	content := "\ufeff" + strings.ReplaceAll(fixtureIssues["030-lectura-de-ejemplo/issue.md"], "\n", "\r\n")
	doc, err := ParseTemplateDoc("/r/030-lectura-de-ejemplo/issue.md", "030-lectura-de-ejemplo", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Number != 30 || doc.SourceKey != "030-lectura-de-ejemplo" || doc.Slug != "lectura-de-ejemplo" || doc.FolderNumber != 0 ||
		doc.RawDate != "2026-01-10" || doc.RawResolved != "" || !strings.HasPrefix(doc.Body, "## Contexto") {
		t.Errorf("doc: %+v", doc)
	}

	// Sin titulo: el del # del cuerpo; sin ninguno: el slug.
	doc, err = ParseTemplateDoc("p", "009-x", []byte("---\nid: 9\n---\n\n# Del cuerpo\n\nTexto\n"))
	if err != nil || doc.Title != "Del cuerpo" || doc.Body != "Texto" {
		t.Errorf("title from body: %+v %v", doc, err)
	}
	doc, err = ParseTemplateDoc("p", "009-x", []byte("---\nid: 9\n---\nTexto\n# No es el título\n"))
	if err != nil || doc.Title != "x" || !strings.Contains(doc.Body, "# No es el título") {
		t.Errorf("title from slug: %+v %v", doc, err)
	}

	// id distinto de la carpeta: manda el id y se avisa.
	doc, err = ParseTemplateDoc("p", "010-x", []byte("---\nid: 11\n---\n"))
	if err != nil || doc.Number != 11 || doc.FolderNumber != 10 {
		t.Errorf("id mismatch: %+v %v", doc, err)
	}

	for name, c := range map[string]string{
		"sin frontmatter":    "# Título\n",
		"sin cerrar":         "---\nid: 1\n",
		"sin id":             "---\ntitulo: x\n---\n",
		"vacío":              "---\n---\n",
		"id 0":               "---\nid: 0\n---\n",
		"yaml inválido":      "---\nid: [1\n---\n",
		"estado no es texto": "---\nid: 1\nestado: [a, b]\n---\n",
	} {
		if _, err := ParseTemplateDoc("p", "001-x", []byte(c)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := ParseTemplateDoc("p", "plantilla", []byte("---\nid: 1\n---\n")); err == nil {
		t.Errorf("folder without number: want error")
	}
	if _, err := ParseTemplateDoc("p", "001-x", []byte{0xff}); err == nil {
		t.Errorf("invalid UTF-8: want error")
	}
}

func TestMapTemplate(t *testing.T) {
	states := map[string]struct {
		state   issuesv1.IssueState
		blocked bool
	}{
		"abierto":     {issuesv1.IssueState_ISSUE_STATE_TODO, false},
		"en-curso":    {issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS, false},
		" Bloqueado ": {issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS, true},
		"resuelto":    {issuesv1.IssueState_ISSUE_STATE_DONE, false},
		"descartado":  {issuesv1.IssueState_ISSUE_STATE_CANCELED, false},
	}
	for raw, want := range states {
		if s, b, ok := MapTemplateState(raw); !ok || s != want.state || b != want.blocked {
			t.Errorf("MapTemplateState(%q) = %v %v %v", raw, s, b, ok)
		}
	}
	for _, raw := range []string{"", "open", "en curso"} {
		if s, _, ok := MapTemplateState(raw); ok || s != issuesv1.IssueState_ISSUE_STATE_TODO {
			t.Errorf("MapTemplateState(%q) = %v %v, want TODO unknown", raw, s, ok)
		}
	}
	prios := map[string]issuesv1.Priority{"alta": issuesv1.Priority_PRIORITY_HIGH, "Media": issuesv1.Priority_PRIORITY_MEDIUM, "baja": issuesv1.Priority_PRIORITY_LOW}
	for raw, want := range prios {
		if p, ok := MapTemplatePriority(raw); !ok || p != want {
			t.Errorf("MapTemplatePriority(%q) = %v %v", raw, p, ok)
		}
	}
	if p, ok := MapTemplatePriority(""); ok || p != issuesv1.Priority_PRIORITY_MEDIUM {
		t.Errorf("empty priority: %v %v", p, ok)
	}

	long := strings.Repeat("a", 50)
	got := TemplateLabels([]string{"Feature", "", "api", "api", " cli ", "null", long, "bloqueado"}, true)
	want := []string{"feature", "api", "cli", strings.Repeat("a", 40), "bloqueado"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TemplateLabels = %v, want %v", got, want)
	}
	if got := TemplateLabels(nil, false); got != nil {
		t.Errorf("no labels: %v", got)
	}
}

func TestTemplateDescription_Truncated(t *testing.T) {
	doc := &Doc{Dir: "001-x", Body: strings.Repeat("ñ", 30000), HasPlan: true}
	desc, truncated := TemplateDescription(doc, "o/r", "https://github.com/o/r/blob/main/contenido/issues/")
	if !truncated || len([]rune(desc)) > DescriptionMaxLen || !strings.HasSuffix(desc, "/001-x/plan.md).") {
		t.Errorf("truncated %v len %d", truncated, len([]rune(desc)))
	}
}

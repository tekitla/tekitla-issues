package main

// Issue 039: categoría (Perfil:) y responsable. Datos inventados.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

func TestCategoryLabels(t *testing.T) {
	cases := map[string][]string{
		"escuela, familia":                  {"perfil/escuela", "perfil/familia"},
		"`Escuela`, **Familia** , Términos": {"perfil/escuela", "perfil/familia", "perfil/terminos"},
		"Admin Round, admin  round":         {"perfil/admin-round"},
		"agente — la parte del chat (QA)":   {"perfil/agente"},
		"recomendador (y landing)":          {"perfil/recomendador"},
		"_(pendiente)_":                     nil,
		"":                                  nil,
		"plataforma,, ,landing":             {"perfil/plataforma", "perfil/landing"},
	}
	for raw, want := range cases {
		if got := CategoryLabels(ProfilePrefix, raw); !reflect.DeepEqual(got, want) {
			t.Errorf("CategoryLabels(%q) = %v, want %v", raw, got, want)
		}
	}
	if got := ResponsibleName("**Ana Pérez** — revisa el diseño"); got != "Ana Pérez" {
		t.Errorf("ResponsibleName = %q", got)
	}
}

// CU-039-5: «Perfil:» con y sin «>» (las dos plantillas de Round).
func TestParseDoc_ProfileAndResponsible(t *testing.T) {
	quoted := "# [901] Algo\n\n> **Estado:** `open`\n> **Perfil:** escuela, familia\n> **Responsable:** Ana Pérez\n\n## Descripción\n\n> **Perfil:** admin\n"
	bare := "# 902 · Otra\n\n**Estado:** open\n**Perfil:** Agente\n\n## Descripción\n"
	d1, err := ParseDoc("/r/901-algo.md", "901-algo.md", []byte(quoted))
	if err != nil {
		t.Fatal(err)
	}
	if got := categoryLabels(d1); !reflect.DeepEqual(got, []string{"perfil/escuela", "perfil/familia"}) {
		t.Errorf("quoted: %v (the body's Perfil does not count)", got)
	}
	if d1.RawResponsible != "Ana Pérez" {
		t.Errorf("responsible %q", d1.RawResponsible)
	}
	d2, err := ParseDoc("/r/902-otra.md", "902-otra.md", []byte(bare))
	if err != nil {
		t.Fatal(err)
	}
	if got := categoryLabels(d2); !reflect.DeepEqual(got, []string{"perfil/agente"}) || d2.RawResponsible != "" {
		t.Errorf("bare: %v %q", got, d2.RawResponsible)
	}
}

func TestTemplateDoc_Categories(t *testing.T) {
	content := "---\nid: 7\ntitulo: X\nestado: abierto\ntipo: feature\narea: web\nperfil: [Escuela, familia]\ncategoria: Pagos y cobros\n---\n# X\n"
	d, err := ParseTemplateDoc("/d/007-x/issue.md", "007-x", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	tr := translateTemplate(d, "o/r", "https://x/")
	for _, want := range []string{"feature", "web", "perfil/escuela", "perfil/familia", "categoria/pagos-y-cobros"} {
		if !slices.Contains(tr.Labels, want) {
			t.Errorf("labels %v: missing %q", tr.Labels, want)
		}
	}
	bad := "---\nid: 7\nperfil: {a: b}\n---\n"
	if _, err := ParseTemplateDoc("/d/007-x/issue.md", "007-x", []byte(bad)); err == nil {
		t.Errorf("a map in perfil must fail")
	}
}

// roundFake suma al fakeIssues el asignado (UpdateIssue) y los actores.
type roundFake struct {
	*fakeIssues
	issuesv1.ActorServiceClient // solo ListActors

	actors   []*issuesv1.Actor
	assignee map[int32]string
	updates  []*issuesv1.UpdateIssueRequest
}

func newRoundFake(actors ...*issuesv1.Actor) *roundFake {
	return &roundFake{fakeIssues: newFake(), actors: actors, assignee: map[int32]string{}}
}

func (f *roundFake) ListIssues(ctx context.Context, req *issuesv1.ListIssuesRequest, opts ...grpc.CallOption) (*issuesv1.ListIssuesResponse, error) {
	res, err := f.fakeIssues.ListIssues(ctx, req, opts...)
	for _, it := range res.GetIssues() {
		it.AssigneeActorId = f.assignee[it.GetNumber()]
	}
	return res, err
}

func (f *roundFake) UpdateIssue(_ context.Context, req *issuesv1.UpdateIssueRequest, _ ...grpc.CallOption) (*issuesv1.UpdateIssueResponse, error) {
	f.updates = append(f.updates, req)
	f.assignee[req.GetNumber()] = req.GetAssigneeActorId()
	return &issuesv1.UpdateIssueResponse{}, nil
}

func (f *roundFake) ListActors(context.Context, *issuesv1.ListActorsRequest, ...grpc.CallOption) (*issuesv1.ListActorsResponse, error) {
	return &issuesv1.ListActorsResponse{Actors: f.actors}, nil
}

func (f *roundFake) labels(key string) []string {
	for _, r := range f.issues["p-RND"] {
		if r.GetSourceKey() == key {
			return r.GetLabelNames()
		}
	}
	return nil
}

func writeRound(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runRound(t *testing.T, f *roundFake, dir, me string) string {
	t.Helper()
	var out strings.Builder
	o := options{dir: dir, user: "u1", me: me, createProject: true, skipGitCheck: true, client: f, actors: f}
	if err := run(context.Background(), o, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	return out.String()
}

func roundIssue(title, extra string) string {
	return "# " + title + "\n\n> **Estado:** `open`\n> **Área principal:** `web`\n" + extra + "\n## Descripción\n\nTexto.\n"
}

var (
	person = &issuesv1.Actor{Id: "a-person", Kind: issuesv1.ActorKind_ACTOR_KIND_PERSON, UserId: "u1"}
	agent  = &issuesv1.Actor{Id: "a-bot", Kind: issuesv1.ActorKind_ACTOR_KIND_AGENT, DisplayName: "Robotina", OwnerActorId: "a-person"}
	gone   = &issuesv1.Actor{Id: "a-gone", Kind: issuesv1.ActorKind_ACTOR_KIND_AGENT, DisplayName: "Viejo", RevokedAt: timestamppb.Now()}
)

// CU-039-5, CU-039-6 y CU-039-9 de punta a punta, dos corridas.
func TestRunRound_ProfilesAndResponsible(t *testing.T) {
	dir := writeRound(t, map[string]string{
		"901-perfiles.md":   roundIssue("Perfiles", "> **Perfil:** escuela, familia\n"),
		"902-agente.md":     roundIssue("Del agente", "> **Responsable:** robotina\n"),
		"903-sin-cuenta.md": roundIssue("Sin cuenta", "**Perfil:** agente\n**Responsable:** Ana Pérez\n"),
		"904-revocado.md":   roundIssue("Revocado", "> **Responsable:** Viejo\n"),
		"905-yo.md":         roundIssue("Mío", "> **Responsable:** Rosa\n"),
	})
	f := newRoundFake(person, agent, gone)
	out := runRound(t, f, dir, "rosa")

	// CU-039-5: etiquetas perfil/ junto al área, sin duplicados.
	if got := f.labels("901-perfiles"); !reflect.DeepEqual(got, []string{"web", "perfil/escuela", "perfil/familia"}) {
		t.Errorf("901 labels %v", got)
	}
	// CU-039-6: coincide con un agente → asignado; sin etiqueta responsable/.
	if f.assignee[902] != "a-bot" || slices.ContainsFunc(f.labels("902-agente"), func(l string) bool { return strings.HasPrefix(l, "responsable/") }) {
		t.Errorf("902: assignee %q labels %v", f.assignee[902], f.labels("902-agente"))
	}
	// CU-039-9: sin cuenta → etiqueta responsable/<nombre>, sin asignar, en el reporte.
	if got := f.labels("903-sin-cuenta"); !reflect.DeepEqual(got, []string{"web", "perfil/agente", "responsable/ana-perez"}) || f.assignee[903] != "" {
		t.Errorf("903: labels %v assignee %q", got, f.assignee[903])
	}
	// Un agente revocado no cuenta: sin cuenta.
	if f.assignee[904] != "" || !slices.Contains(f.labels("904-revocado"), "responsable/viejo") {
		t.Errorf("904: assignee %q labels %v", f.assignee[904], f.labels("904-revocado"))
	}
	// -yo: el nombre de quien importa se le asigna a su persona.
	if f.assignee[905] != "a-person" {
		t.Errorf("905: assignee %q", f.assignee[905])
	}
	for _, want := range []string{"Asignados por «Responsable:» (2)", "RND-902", "-> Robotina", "Responsable sin cuenta", "903-sin-cuenta.md  [Ana Pérez]", "904-revocado.md  [Viejo]"} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q:\n%s", want, out)
		}
	}
	if len(f.updates) != 2 {
		t.Fatalf("updates: %d, want 2", len(f.updates))
	}

	// Segunda corrida: nada cambia, nada se escribe.
	out = runRound(t, f, dir, "rosa")
	if len(f.updates) != 2 || !strings.Contains(out, "sin cambios: 5") || strings.Contains(out, "Asignados") {
		t.Errorf("second run must not write (updates %d):\n%s", len(f.updates), out)
	}

	// Si el asignado se cambió a mano y el doc no tiene responsable, no se
	// toca; si el doc lo tiene, el doc manda (RND es un espejo).
	f.assignee[901], f.assignee[902] = "a-person", "a-person"
	runRound(t, f, dir, "rosa")
	if f.assignee[901] != "a-person" || f.assignee[902] != "a-bot" {
		t.Errorf("assignees after manual change: 901 %q, 902 %q", f.assignee[901], f.assignee[902])
	}
}

// Sin «Responsable:» en ningún doc no se piden los actores (el fake sin
// ActorService entra en pánico si se llama).
func TestRunRound_NoResponsibleSkipsActors(t *testing.T) {
	dir := writeRound(t, map[string]string{"901-x.md": roundIssue("X", "> **Perfil:** escuela\n")})
	f := newRoundFake()
	var out strings.Builder
	o := options{dir: dir, user: "u1", createProject: true, skipGitCheck: true, client: f}
	if err := run(context.Background(), o, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRunRound_DryRunReportsNoAccount(t *testing.T) {
	dir := writeRound(t, map[string]string{"903-x.md": roundIssue("X", "> **Responsable:** Ana Pérez\n")})
	var out strings.Builder
	if err := run(context.Background(), options{dir: dir, dryRun: true, skipGitCheck: true}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "903-x.md  [Ana Pérez]") {
		t.Errorf("dry-run:\n%s", out.String())
	}
}

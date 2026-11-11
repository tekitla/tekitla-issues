package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

// Fixtures inventados (issue 036): ningún dato real de Round.

// testRepo es un repo git temporal con Issues/ en la raíz.
type testRepo struct {
	t    *testing.T
	root string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("sin git")
	}
	r := &testRepo{t: t, root: t.TempDir()}
	if err := os.MkdirAll(r.issues(), 0o755); err != nil {
		t.Fatal(err)
	}
	r.git("", "init", "-q", "-b", "main")
	return r
}

func (r *testRepo) issues() string { return filepath.Join(r.root, "Issues") }

// git corre git en el repo con una fecha fija para el commit ("" = sin
// fecha) y sin la configuración de la compu.
func (r *testRepo) git(date string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.root, "-c", "user.name=Prueba", "-c", "user.email=prueba@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if date != "" {
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit escribe los archivos (nombre -> contenido; "" lo borra) y hace un
// commit con esa fecha. Devuelve el sha.
func (r *testRepo) commit(date string, files map[string]string) string {
	r.t.Helper()
	for name, content := range files {
		path := filepath.Join(r.issues(), name)
		if content == "" {
			r.git("", "rm", "-q", "Issues/"+name)
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("", "add", "-A")
	r.git(date, "commit", "-q", "-m", "cambio")
	return r.git("", "rev-parse", "HEAD")
}

// mv renombra un archivo con un commit.
func (r *testRepo) mv(date, from, to string) string {
	r.t.Helper()
	r.git("", "mv", "Issues/"+from, "Issues/"+to)
	r.git(date, "commit", "-q", "-m", "renombrar")
	return r.git("", "rev-parse", "HEAD")
}

// issueMD arma un issue con la plantilla de cabecera en cita.
func issueMD(title, state, detected, resolved, body string) string {
	return "# " + title + "\n\n" +
		"> **Estado:** " + state + "\n" +
		"> **Área principal:** `infra`\n" +
		"> **Prioridad:** `media`\n" +
		"> **Fecha detectado:** " + detected + "\n" +
		"> **Fecha resuelto:** " + resolved + "\n\n---\n\n## Descripción\n\n" + body + "\n"
}

// history arma el histórico de un archivo del repo como lo hace runHistory.
func (r *testRepo) history(file string) History {
	r.t.Helper()
	content, err := os.ReadFile(filepath.Join(r.issues(), file))
	if err != nil {
		r.t.Fatal(err)
	}
	doc, err := ParseDoc(filepath.Join(r.issues(), file), file, content)
	if err != nil {
		r.t.Fatal(err)
	}
	blobs, err := newBlobReader(r.issues())
	if err != nil {
		r.t.Fatal(err)
	}
	defer blobs.Close()
	logs, err := folderLog(r.issues(), []string{file})
	if err != nil {
		r.t.Fatal(err)
	}
	return BuildHistory(doc, readStates(blobs, doc, logs[file]))
}

func day(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

const (
	todo     = issuesv1.IssueState_ISSUE_STATE_TODO
	progress = issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS
	done     = issuesv1.IssueState_ISSUE_STATE_DONE
	canceled = issuesv1.IssueState_ISSUE_STATE_CANCELED
)

// CU-036-1: cada commit que cambia el valor del Estado es un StatusChanged
// con la fecha del commit; los que solo cambian el texto alrededor o el
// cuerpo no.
func TestHistory_TransitionsFromCommits(t *testing.T) {
	r := newTestRepo(t)
	const f = "007-cola-que-se-traba.md"
	c1 := r.commit("2026-06-18T10:00:00-06:00", map[string]string{f: issueMD("La cola se traba", "`open`", "2026-06-18", "_(vacío)_", "Uno.")})
	c2 := r.commit("2026-07-01T09:30:00-06:00", map[string]string{f: issueMD("La cola se traba", "`in-progress`", "2026-06-18", "_(vacío)_", "Uno.")})
	r.commit("2026-07-03T12:00:00-06:00", map[string]string{f: issueMD("La cola se traba", "`in-progress`", "2026-06-18", "_(vacío)_", "Dos.")})
	c4 := r.commit("2026-07-10T18:00:00-06:00", map[string]string{f: issueMD("La cola se traba", "`resolved` — en la rama x", "2026-06-18", "2026-07-10", "Dos.")})
	r.commit("2026-07-12T08:00:00-06:00", map[string]string{f: issueMD("La cola se traba", "`resolved` — mergeado", "2026-06-18", "2026-07-10", "Dos.")})

	h := r.history(f)
	want := []HistoryEvent{
		{Key: "007-cola-que-se-traba#alta", Kind: KindCreated, Date: day("2026-06-18T10:00:00-06:00"), Source: SourceCommit, Commit: c1},
		{Key: "007-cola-que-se-traba#" + c2, Kind: KindStatus, Date: day("2026-07-01T09:30:00-06:00"), Source: SourceCommit, Commit: c2,
			FromRaw: "`open`", ToRaw: "`in-progress`", From: todo, To: progress},
		{Key: "007-cola-que-se-traba#" + c4, Kind: KindStatus, Date: day("2026-07-10T18:00:00-06:00"), Source: SourceCommit, Commit: c4,
			FromRaw: "`in-progress`", ToRaw: "`resolved` — en la rama x", From: progress, To: done},
	}
	assertEvents(t, h.Events, want)
	if h.CreatedFromFile || h.ClosedFromFile || h.DetectedAfterFirstCommit || h.ResolvedIgnored != "" {
		t.Errorf("flags: %+v", h)
	}
}

// CU-036-2: un issue que nace cerrado (ningún commit lo marca) cierra con
// su «Fecha resuelto», con fuente archivo; «Fecha detectado» anterior al
// primer commit es el alta.
func TestHistory_DatesFromFile(t *testing.T) {
	r := newTestRepo(t)
	const f = "003-login-lento.md"
	r.commit("2026-06-18T10:00:00-06:00", map[string]string{f: issueMD("Login lento", "`resolved`", "2026-05-02", "2026-05-20", "x")})

	h := r.history(f)
	assertEvents(t, h.Events, []HistoryEvent{
		{Key: "003-login-lento#alta", Kind: KindCreated, Date: day("2026-05-02T00:00:00Z"), Source: SourceFile},
		{Key: "003-login-lento#inicial", Kind: KindStatus, Date: day("2026-05-20T00:00:00Z"), Source: SourceFile,
			FromRaw: "open", ToRaw: "`resolved`", From: todo, To: done},
	})
	if !h.CreatedFromFile || !h.ClosedFromFile {
		t.Errorf("flags: %+v", h)
	}
}

// Nace cerrado sin «Fecha resuelto»: cierre en el primer commit.
func TestHistory_BornClosedWithoutResolvedDate(t *testing.T) {
	r := newTestRepo(t)
	const f = "004-sin-fecha.md"
	c1 := r.commit("2026-06-18T10:00:00Z", map[string]string{f: issueMD("Sin fecha", "`won't-do`", "2026-06-18", "_(vacío si open)_", "x")})

	h := r.history(f)
	assertEvents(t, h.Events, []HistoryEvent{
		{Key: "004-sin-fecha#alta", Kind: KindCreated, Date: day("2026-06-18T10:00:00Z"), Source: SourceCommit, Commit: c1},
		{Key: "004-sin-fecha#inicial", Kind: KindStatus, Date: day("2026-06-18T10:00:00Z"), Source: SourceCommit, Commit: c1,
			FromRaw: "open", ToRaw: "`won't-do`", From: todo, To: canceled},
	})
	if h.ClosedFromFile || h.ResolvedIgnored != "" {
		t.Errorf("flags: %+v", h)
	}
}

// Un commit que cierra manda sobre «Fecha resuelto»; una «Fecha resuelto»
// en un issue abierto o incoherente se reporta y no se usa.
func TestHistory_ResolvedDateIgnored(t *testing.T) {
	r := newTestRepo(t)
	r.commit("2026-06-18T10:00:00Z", map[string]string{
		"010-reabierto.md":      issueMD("Reabierto", "`resolved`", "2026-06-01", "2026-06-10", "x"),
		"011-antes-del-alta.md": issueMD("Antes del alta", "`done`", "2026-06-05", "2026-06-01", "x"),
		"012-despues.md":        issueMD("Después", "`done`", "2026-06-05", "2026-07-30", "x"),
	})
	r.commit("2026-07-01T10:00:00Z", map[string]string{"010-reabierto.md": issueMD("Reabierto", "`open`", "2026-06-01", "2026-06-10", "x")})

	// Nace cerrado: el primer cierre usa la fecha del archivo; después se reabre.
	if h := r.history("010-reabierto.md"); h.ResolvedIgnored != "" || !h.ClosedFromFile || len(h.Events) != 3 || h.Events[2].To != todo {
		t.Errorf("010: %+v", h)
	}
	if h := r.history("011-antes-del-alta.md"); h.ResolvedIgnored != "anterior al alta" || h.ClosedFromFile {
		t.Errorf("011: %+v", h)
	}
	if h := r.history("012-despues.md"); !strings.HasPrefix(h.ResolvedIgnored, "posterior") || h.ClosedFromFile {
		t.Errorf("012: %+v", h)
	}
}

// Renombrar el archivo no corta el histórico: se sigue con --follow y la
// clave es la del nombre actual.
func TestHistory_FollowsRenames(t *testing.T) {
	r := newTestRepo(t)
	r.commit("2026-06-18T10:00:00Z", map[string]string{"003-checklist.md": issueMD("Checklist", "`open`", "2026-06-18", "", "x")})
	c2 := r.commit("2026-06-20T10:00:00Z", map[string]string{"003-checklist.md": issueMD("Checklist", "`blocked`", "2026-06-18", "", "x")})
	r.mv("2026-06-25T10:00:00Z", "003-checklist.md", "251-checklist.md")
	c4 := r.commit("2026-06-30T10:00:00Z", map[string]string{"251-checklist.md": issueMD("Checklist", "`done`", "2026-06-18", "2026-06-30", "x")})

	h := r.history("251-checklist.md")
	if len(h.Events) != 3 || h.Events[1].Key != "251-checklist#"+c2 || h.Events[2].Key != "251-checklist#"+c4 || h.Events[2].From != progress || h.Events[2].To != done {
		t.Fatalf("events: %+v", h.Events)
	}
	if len(h.Unreadable) != 0 {
		t.Errorf("unreadable: %+v", h.Unreadable)
	}
}

// Un Estado desconocido en un commit se reporta y cuenta como TODO; uno
// sin línea Estado se salta.
func TestHistory_UnknownAndMissingState(t *testing.T) {
	r := newTestRepo(t)
	const f = "020-raro.md"
	r.commit("2026-06-18T10:00:00Z", map[string]string{f: issueMD("Raro", "`open`", "2026-06-18", "", "x")})
	r.commit("2026-06-19T10:00:00Z", map[string]string{f: "# Raro\n\nsin cabecera\n"})
	c3 := r.commit("2026-06-20T10:00:00Z", map[string]string{f: issueMD("Raro", "`en-pausa`", "2026-06-18", "", "x")})
	r.commit("2026-06-21T10:00:00Z", map[string]string{f: issueMD("Raro", "`en-pausa`", "2026-06-18", "", "y")})

	h := r.history(f)
	if len(h.Events) != 2 || h.Events[1].Commit != c3 || h.Events[1].To != todo {
		t.Fatalf("events: %+v", h.Events)
	}
	if len(h.Unknown) != 1 || h.Unknown[0].Commit != c3 {
		t.Errorf("unknown: %+v", h.Unknown)
	}
}

// CU-036-1: dos corridas dan las mismas claves, y con lo ya cargado no
// queda nada pendiente.
func TestHistory_Idempotent(t *testing.T) {
	r := newTestRepo(t)
	const f = "007-cola.md"
	r.commit("2026-06-18T10:00:00Z", map[string]string{f: issueMD("Cola", "`open`", "2026-06-01", "", "x")})
	r.commit("2026-07-01T10:00:00Z", map[string]string{f: issueMD("Cola", "`resolved`", "2026-06-01", "2026-07-01", "x")})

	first := r.history(f)
	loaded := map[string]bool{}
	for _, e := range first.Pending(loaded) {
		loaded[e.Key] = true
	}
	if len(loaded) != 2 {
		t.Fatalf("primera corrida: %d eventos", len(loaded))
	}
	second := r.history(f)
	if p := second.Pending(loaded); len(p) != 0 {
		t.Errorf("segunda corrida agrega %+v", p)
	}
	assertEvents(t, second.Events, first.Events)

	// Un commit nuevo agrega solo su evento.
	c3 := r.commit("2026-07-05T10:00:00Z", map[string]string{f: issueMD("Cola", "`open`", "2026-06-01", "", "reabierto")})
	if p := r.history(f).Pending(loaded); len(p) != 1 || p[0].Commit != c3 {
		t.Errorf("después de reabrir: %+v", p)
	}
}

// El modo -historico de punta a punta: reporte con los totales, sin el
// archivo que pierde el número repetido; sin -dry-run no corre.
func TestRunHistory(t *testing.T) {
	r := newTestRepo(t)
	r.commit("2026-06-18T10:00:00Z", map[string]string{
		"001-a.md":   issueMD("A", "`open`", "2026-06-01", "", "x"),
		"002-b.md":   issueMD("B", "`resolved`", "2026-06-01", "2026-06-10", "x"),
		"002-zzz.md": issueMD("Z", "`open`", "2026-06-01", "", "x"),
		"README.md":  "# no es un issue\n",
	})
	r.commit("2026-07-01T10:00:00Z", map[string]string{"001-a.md": issueMD("A", "`in-progress`", "2026-06-01", "", "x")})

	var out strings.Builder
	o := options{dir: r.issues(), history: true, dryRun: true, skipGitCheck: true, detail: true}
	if err := run(context.Background(), o, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"== import-round -historico (dry-run",
		"Archivos leídos: 3 · con histórico: 2 · omitidos por número: 1",
		"Eventos: 4 (altas: 2 · cambios de Estado: 2, 2 con cambio de categoría) · de commits: 1 · del archivo: 3",
		"histórico aproximado",
		"-- Cierre tomado de «Fecha resuelto» (ningún commit lo marca) (1)",
		"RND-2  " + filepath.Join(r.issues(), "002-b.md"),
		"queda afuera: " + filepath.Join(r.issues(), "002-zzz.md"),
		"2026-07-01  TODO → IN_PROGRESS  [open → in-progress]  (commit ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en:\n%s", want, got)
		}
	}

	o.dryRun = false
	if err := run(context.Background(), o, &out); err != errHistoryNeedsContract {
		t.Errorf("sin -dry-run: %v", err)
	}
	if err := run(context.Background(), options{dir: r.issues(), dryRun: true, skipGitCheck: true, detail: true}, &out); err == nil {
		t.Error("-detalle sin -historico debería fallar")
	}
	o.dryRun, o.format, o.repo, o.projectKey = true, formatTemplateDocs, "prueba/docs", "PR"
	if err := run(context.Background(), o, &out); err == nil || !strings.Contains(err.Error(), "-historico") {
		t.Errorf("-historico con template-docs: %v", err)
	}
}

func assertEvents(t *testing.T, got, want []HistoryEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("eventos: %d, quiero %d\n%+v", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if !g.Date.Equal(w.Date) {
			t.Errorf("evento %d: fecha %v, quiero %v", i, g.Date, w.Date)
		}
		g.Date, w.Date = time.Time{}, time.Time{}
		if g != w {
			t.Errorf("evento %d:\n got %+v\nwant %+v", i, g, w)
		}
	}
}

// Solo la historia de main: dos ramas que tocan el mismo archivo no se
// intercalan. El cambio de cada rama cuenta en su merge, con su fecha.
func TestHistory_FirstParentOnly(t *testing.T) {
	r := newTestRepo(t)
	const f = "030-ramas.md"
	r.commit("2026-06-18T10:00:00Z", map[string]string{f: issueMD("Ramas", "`open`", "2026-06-18", "", "x")})
	// Rama a: pasa a in-progress el 2026-06-20.
	r.git("", "checkout", "-q", "-b", "a")
	r.commit("2026-06-20T10:00:00Z", map[string]string{f: issueMD("Ramas", "`in-progress`", "2026-06-18", "", "x")})
	// Rama b (desde main): edita el cuerpo el 2026-06-21, con Estado open.
	r.git("", "checkout", "-q", "main")
	r.git("", "checkout", "-q", "-b", "b")
	r.commit("2026-06-21T10:00:00Z", map[string]string{"031-otro.md": issueMD("Otro", "`open`", "2026-06-18", "", "x")})
	r.commit("2026-06-21T11:00:00Z", map[string]string{f: issueMD("Ramas", "`open`", "2026-06-18", "", "x\n\notro párrafo")})
	r.git("", "checkout", "-q", "main")
	r.git("2026-06-22T10:00:00Z", "merge", "-q", "--no-ff", "-m", "merge a", "a")
	m2 := r.git("", "rev-parse", "HEAD")
	// Merge de b: sin conflicto (b no tocó la línea Estado), queda in-progress.
	r.git("2026-06-23T10:00:00Z", "merge", "-q", "--no-ff", "-m", "merge b", "b")

	h := r.history(f)
	if len(h.Events) != 2 {
		t.Fatalf("una sola transición, quiero alta + 1: %+v", h.Events)
	}
	e := h.Events[1]
	if e.Commit != m2 || e.From != todo || e.To != progress || !e.Date.Equal(day("2026-06-22T10:00:00Z")) {
		t.Errorf("transición: %+v", e)
	}
}

// El primer commit con Estado no es el primero del archivo: el cierre del
// archivo nacido cerrado igual usa «Fecha resuelto».
func TestHistory_FirstStateLaterThanFirstCommit(t *testing.T) {
	r := newTestRepo(t)
	const f = "040-tarde.md"
	r.commit("2026-06-18T10:00:00Z", map[string]string{f: "# Tarde\n\nborrador sin cabecera\n"})
	r.commit("2026-06-19T10:00:00Z", map[string]string{f: issueMD("Tarde", "`resolved`", "2026-06-01", "2026-06-10", "x")})

	h := r.history(f)
	if !h.ClosedFromFile || len(h.Events) != 2 || h.Events[1].Source != SourceFile || !h.Events[1].Date.Equal(day("2026-06-10T00:00:00Z")) {
		t.Errorf("history: %+v", h)
	}
}

// Una copia superficial daría un histórico falso: -historico no corre.
func TestHistory_RefusesShallowClone(t *testing.T) {
	r := newTestRepo(t)
	r.commit("2026-06-18T10:00:00Z", map[string]string{"001-a.md": issueMD("A", "`open`", "2026-06-18", "", "x")})
	r.commit("2026-06-19T10:00:00Z", map[string]string{"001-a.md": issueMD("A", "`done`", "2026-06-18", "", "x")})
	clone := filepath.Join(t.TempDir(), "clon")
	r.git("", "clone", "-q", "--depth", "1", "file://"+filepath.ToSlash(r.root), clone)
	_, err := folderLog(filepath.Join(clone, "Issues"), []string{"001-a.md"})
	if err == nil || !strings.Contains(err.Error(), "superficial") {
		t.Errorf("err: %v", err)
	}
}

// Pedir algo que no es un archivo (un árbol) no desfasa las lecturas
// siguientes.
func TestBlobReader_NonBlobKeepsInSync(t *testing.T) {
	r := newTestRepo(t)
	c := r.commit("2026-06-18T10:00:00Z", map[string]string{"001-a.md": "hola\n"})
	blobs, err := newBlobReader(r.issues())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.read(c, "Issues"); err == nil || !strings.Contains(err.Error(), "tree") {
		t.Errorf("árbol: %v", err)
	}
	if _, err := blobs.read(c, "Issues/no-existe.md"); err == nil {
		t.Error("ruta inexistente sin error")
	}
	if b, err := blobs.read(c, "Issues/001-a.md"); err != nil || string(b) != "hola\n" {
		t.Errorf("blob: %q %v", b, err)
	}
	if err := blobs.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestUnquotePath(t *testing.T) {
	for in, want := range map[string]string{
		`Issues/001-a.md`:          "Issues/001-a.md",
		`"Issues/001-a\"b.md"`:     `Issues/001-a"b.md`,
		`"Issues/001-a\\b.md"`:     `Issues/001-a\b.md`,
		`"Issues/001-a\tb.md"`:     "Issues/001-a\tb.md",
		`"Issues/001-\303\251.md"`: "Issues/001-é.md",
	} {
		if got := unquotePath(in); got != want {
			t.Errorf("unquotePath(%s) = %q, quiero %q", in, got, want)
		}
	}
}

// Un valor desconocido que vuelve después de otro se reporta una sola vez.
func TestHistory_UnknownOncePerValue(t *testing.T) {
	r := newTestRepo(t)
	const f = "050-vuelve.md"
	r.commit("2026-06-18T10:00:00Z", map[string]string{f: issueMD("Vuelve", "`abierto`", "2026-06-18", "", "x")})
	r.commit("2026-06-19T10:00:00Z", map[string]string{f: issueMD("Vuelve", "`in-progress`", "2026-06-18", "", "x")})
	r.commit("2026-06-20T10:00:00Z", map[string]string{f: issueMD("Vuelve", "`abierto`", "2026-06-18", "", "x")})
	if h := r.history(f); len(h.Unknown) != 1 || len(h.Events) != 3 {
		t.Errorf("unknown %+v, events %d", h.Unknown, len(h.Events))
	}
}

// Cierre del archivo el mismo día del alta por commit: no queda antes del
// alta (el contrato no acepta eventos anteriores al created_at).
func TestHistory_FileCloseNotBeforeCreated(t *testing.T) {
	r := newTestRepo(t)
	const f = "060-mismo-dia.md"
	r.commit("2026-06-18T10:00:00Z", map[string]string{f: issueMD("Mismo día", "`done`", "2026-06-18", "2026-06-18", "x")})
	h := r.history(f)
	if !h.ClosedFromFile || len(h.Events) != 2 || !h.Events[1].Date.Equal(h.Events[0].Date) {
		t.Errorf("history: %+v", h)
	}
}

// Cierre del archivo con la fecha del día del commit en una zona adelantada:
// no queda después del commit que lo trae cerrado.
func TestHistory_FileCloseNotAfterCommit(t *testing.T) {
	r := newTestRepo(t)
	const f = "070-zona.md"
	r.commit("2026-06-19T00:30:00+14:00", map[string]string{f: issueMD("Zona", "`done`", "2026-06-01", "2026-06-19", "x")})
	h := r.history(f)
	if !h.ClosedFromFile || len(h.Events) != 2 || !h.Events[1].Date.Equal(day("2026-06-19T00:30:00+14:00")) {
		t.Errorf("history: %+v", h.Events)
	}
}

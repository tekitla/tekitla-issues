// import-round importa los issues de Round (los markdown de
// documentacion/Issues) al proyecto RND de Tekitla con ImportIssue (issue
// 025, §4). Lee en solo lectura: no escribe nada en el repo de Round.
//
// Se puede correr de nuevo: la clave de origen (número + slug) reconoce cada
// issue y una corrida sin cambios no crea ni cambia nada.
//
//	go run ./tools/import-round -user <uid> [-addr localhost:5000] [-create-project]
//	go run ./tools/import-round -dry-run            # sin servidor: solo lee y reporta
//
// Habla gRPC sin TLS con el servicio (local, o por un túnel). Correrlo
// contra QA o prod lo decide una persona: no hay valores por defecto que
// apunten ahí.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

// sourceBaseURL es donde vive cada archivo en GitLab (enlace de la
// descripción).
const sourceBaseURL = "https://gitlab.com/roel-education/documentacion/-/blob/main/Issues/"

type options struct {
	dir           string
	addr          string
	user          string
	projectKey    string
	createProject bool
	dryRun        bool
	skipGitCheck  bool
}

func main() {
	var o options
	flag.StringVar(&o.dir, "dir", `C:\dev\round\docs\Issues`, "carpeta Issues del repo documentacion de Round (rama main)")
	flag.StringVar(&o.addr, "addr", "localhost:5000", "dirección gRPC de tekitla-issues")
	flag.StringVar(&o.user, "user", "", "uid de Firebase de quien importa (x-user-id); obligatorio salvo con -dry-run")
	flag.StringVar(&o.projectKey, "project", "RND", "clave del proyecto destino")
	flag.BoolVar(&o.createProject, "create-project", false, "crear el proyecto «Round» si no existe")
	flag.BoolVar(&o.dryRun, "dry-run", false, "solo leer y reportar, sin llamar al servicio")
	flag.BoolVar(&o.skipGitCheck, "skip-git-check", false, "no verificar que la copia de Round esté en main y al día")
	flag.Parse()

	if err := run(context.Background(), o, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "import-round:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options, out io.Writer) error {
	if !o.skipGitCheck {
		if err := checkGit(o.dir); err != nil {
			return err
		}
	}
	docs, broken, err := readDocs(o.dir)
	if err != nil {
		return err
	}
	rep := &Report{Key: strings.ToUpper(o.projectKey), Broken: broken, Total: len(docs)}
	if o.dryRun {
		plan := MakePlan(docs, Existing{})
		rep.Plan = plan
		for _, d := range plan.Import {
			rep.inspect(d)
		}
		rep.Write(out, true)
		return nil
	}
	if o.user == "" {
		return errors.New("falta -user (uid de quien importa)")
	}

	conn, err := grpc.NewClient(o.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("conectar a %s: %w", o.addr, err)
	}
	defer conn.Close()
	client := issuesv1.NewIssueServiceClient(conn)
	ctx = metadata.AppendToOutgoingContext(ctx, "x-user-id", o.user, "x-request-id", "import-round-"+time.Now().UTC().Format("20060102T150405Z"))

	project, err := findProject(ctx, client, o.projectKey, o.createProject)
	if err != nil {
		return err
	}
	existing, err := existingIssues(ctx, client, project.GetId())
	if err != nil {
		return err
	}
	plan := MakePlan(docs, existing)
	rep.Plan = plan
	for _, d := range plan.Import {
		req := rep.inspect(d)
		req.ProjectId = project.GetId()
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		res, err := client.ImportIssue(callCtx, req)
		cancel()
		if err != nil {
			rep.Failed = append(rep.Failed, Failure{Doc: d, Err: status.Convert(err).Message() + " (" + status.Code(err).String() + ")"})
			continue
		}
		switch res.GetOutcome() {
		case issuesv1.ImportOutcome_IMPORT_OUTCOME_CREATED:
			rep.Created = append(rep.Created, d)
		case issuesv1.ImportOutcome_IMPORT_OUTCOME_UPDATED:
			rep.Updated = append(rep.Updated, d)
		default:
			rep.Unchanged++
		}
	}
	rep.Write(out, false)
	return nil
}

// checkGit verifica, sin modificar nada, que la copia de Round esté en main,
// sin cambios en Issues/ y con el mismo commit que origin/main en el remoto
// (git ls-remote no toca la copia).
func checkGit(dir string) error {
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		b, err := cmd.Output()
		if err != nil {
			err = fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(string(b)), err
	}
	branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("%s no es un repo git (usa -skip-git-check si es a propósito): %w", dir, err)
	}
	if branch != "main" {
		return fmt.Errorf("la copia de Round está en %q, no en main: solo se importa main", branch)
	}
	if dirty, err := git("status", "--porcelain", "--", "."); err != nil || dirty != "" {
		return fmt.Errorf("la carpeta tiene cambios sin commitear: se importa solo lo que está en main")
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	remote, err := git("ls-remote", "origin", "refs/heads/main")
	if err != nil {
		return fmt.Errorf("no pude consultar origin/main (sin red?): %w", err)
	}
	if f := strings.Fields(remote); len(f) == 0 || f[0] != head {
		return fmt.Errorf("la copia de Round no está al día con origin/main (local %s, remoto %s): hace falta un pull", short(head), short(remote))
	}
	return nil
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// readDocs lee los issues de dir. Un archivo roto no frena a los demás: va
// en broken. Los .md que no son issues (README, _template) se ignoran.
func readDocs(dir string) ([]*Doc, []Failure, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("leer %s: %w", dir, err)
	}
	var docs []*Doc
	var broken []Failure
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if _, _, ok := ParseName(e.Name()); !ok {
			continue
		}
		path := filepath.Join(dir, e.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			broken = append(broken, Failure{Doc: &Doc{Path: path, File: e.Name()}, Err: err.Error()})
			continue
		}
		doc, err := ParseDoc(path, e.Name(), content)
		if err != nil {
			broken = append(broken, Failure{Doc: &Doc{Path: path, File: e.Name()}, Err: err.Error()})
			continue
		}
		docs = append(docs, doc)
	}
	return docs, broken, nil
}

func findProject(ctx context.Context, client issuesv1.IssueServiceClient, key string, create bool) (*issuesv1.Project, error) {
	cursor := ""
	for {
		res, err := client.ListProjects(ctx, &issuesv1.ListProjectsRequest{Page: &issuesv1.PageRequest{Cursor: cursor, Limit: 100}})
		if err != nil {
			return nil, fmt.Errorf("ListProjects: %w", err)
		}
		for _, p := range res.GetProjects() {
			if strings.EqualFold(p.GetKey(), key) {
				return p, nil
			}
		}
		if !res.GetPage().GetHasNext() {
			break
		}
		cursor = res.GetPage().GetNextCursor()
	}
	if !create {
		return nil, fmt.Errorf("no existe el proyecto %s (usa -create-project para crearlo)", key)
	}
	res, err := client.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: key, Name: "Round",
		Description: "Espejo de los issues de Round (documentacion/Issues), importados con tools/import-round."})
	if err != nil {
		return nil, fmt.Errorf("CreateProject: %w", err)
	}
	return res.GetProject(), nil
}

func existingIssues(ctx context.Context, client issuesv1.IssueServiceClient, projectID string) (Existing, error) {
	existing := Existing{}
	cursor := ""
	for {
		res, err := client.ListIssues(ctx, &issuesv1.ListIssuesRequest{ProjectId: projectID, Page: &issuesv1.PageRequest{Cursor: cursor, Limit: 100}})
		if err != nil {
			return nil, fmt.Errorf("ListIssues: %w", err)
		}
		for _, it := range res.GetIssues() {
			existing[it.GetNumber()] = it.GetSourceKey()
		}
		if !res.GetPage().GetHasNext() {
			return existing, nil
		}
		cursor = res.GetPage().GetNextCursor()
	}
}

// Failure es un archivo que no se pudo leer o importar.
type Failure struct {
	Doc *Doc
	Err string
}

// Finding es un valor de la cabecera que no se reconoció.
type Finding struct {
	Doc   *Doc
	Value string
}

// Report junta lo que pasó en la corrida.
type Report struct {
	// Key es la clave del proyecto destino (para «RND-236»).
	Key       string
	Total     int
	Plan      Plan
	Created   []*Doc
	Updated   []*Doc
	Unchanged int
	Failed    []Failure
	Broken    []Failure

	UnknownState    []Finding
	UnknownPriority []Finding
	NoDate          []*Doc
	NoArea          []*Doc
	Truncated       []*Doc
}

// inspect arma el request de un archivo y anota lo que no se reconoció.
func (r *Report) inspect(d *Doc) *issuesv1.ImportIssueRequest {
	state, ok := MapState(d.RawState)
	if !ok {
		r.UnknownState = append(r.UnknownState, Finding{Doc: d, Value: d.RawState})
	}
	priority, ok := MapPriority(d.RawPriority)
	if !ok {
		r.UnknownPriority = append(r.UnknownPriority, Finding{Doc: d, Value: d.RawPriority})
	}
	desc, truncated := Description(d, sourceBaseURL+d.File)
	if truncated {
		r.Truncated = append(r.Truncated, d)
	}
	req := &issuesv1.ImportIssueRequest{
		SourceKey: d.SourceKey, Number: d.Number, Title: d.Title, Description: desc,
		Priority: priority, State: state,
	}
	if t, ok := ParseDate(d.RawDate); ok {
		req.CreatedAt = timestamppb.New(t)
	} else {
		r.NoDate = append(r.NoDate, d)
	}
	if label := AreaLabel(d.RawArea); label != "" {
		req.LabelNames = []string{label}
	} else {
		r.NoArea = append(r.NoArea, d)
	}
	return req
}

// Write imprime el reporte: cada caso con su ruta.
func (r *Report) Write(w io.Writer, dryRun bool) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }
	if dryRun {
		p("== import-round (dry-run: no se llamó al servicio)")
	} else {
		p("== import-round")
	}
	p("Archivos leídos: %d · a importar: %d · omitidos por número: %d · rotos: %d",
		r.Total, len(r.Plan.Import), countLosers(r.Plan.Duplicates), len(r.Broken))
	if !dryRun {
		p("Importados: %d · actualizados: %d · sin cambios: %d · con error: %d",
			len(r.Created), len(r.Updated), r.Unchanged, len(r.Failed))
	}
	section := func(title string, n int) bool {
		if n == 0 {
			return false
		}
		p("\n-- %s (%d)", title, n)
		return true
	}
	if section("Importados", len(r.Created)) {
		for _, d := range r.Created {
			p("  %s-%d  %s", r.Key, d.Number, d.Path)
		}
	}
	if section("Actualizados", len(r.Updated)) {
		for _, d := range r.Updated {
			p("  %s-%d  %s", r.Key, d.Number, d.Path)
		}
	}
	if section("Números repetidos", len(r.Plan.Duplicates)) {
		for _, d := range r.Plan.Duplicates {
			if d.Winner != nil {
				p("  %s-%d se queda con %s", r.Key, d.Number, d.Winner.Path)
			} else if d.HolderKey != "" {
				p("  %s-%d ya es de %q (sin archivo): ningún archivo se lo queda", r.Key, d.Number, d.HolderKey)
			} else {
				p("  %s-%d ya es de un issue creado en Tekitla: ningún archivo se lo queda", r.Key, d.Number)
			}
			for _, l := range d.Losers {
				p("    queda afuera: %s", l.Path)
			}
		}
	}
	if section("Sin archivo (importados antes; no se borran)", len(r.Plan.Orphans)) {
		for _, o := range r.Plan.Orphans {
			p("  %s-%d  %s", r.Key, o.Number, o.SourceKey)
		}
	}
	if section("Estado desconocido o sin estado (van como TODO)", len(r.UnknownState)) {
		for _, f := range sortFindings(r.UnknownState) {
			p("  %s  [%s]", f.Doc.Path, valueOr(f.Value))
		}
	}
	if section("Prioridad desconocida o sin prioridad (van como MEDIUM)", len(r.UnknownPriority)) {
		for _, f := range sortFindings(r.UnknownPriority) {
			p("  %s  [%s]", f.Doc.Path, valueOr(f.Value))
		}
	}
	if section("Sin fecha de alta (van con la fecha de hoy)", len(r.NoDate)) {
		for _, d := range r.NoDate {
			p("  %s", d.Path)
		}
	}
	if section("Sin área (sin etiqueta)", len(r.NoArea)) {
		for _, d := range r.NoArea {
			p("  %s", d.Path)
		}
	}
	if section("Descripción recortada (más de 20000 caracteres)", len(r.Truncated)) {
		for _, d := range r.Truncated {
			p("  %s", d.Path)
		}
	}
	if section("Con error al importar", len(r.Failed)) {
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

func countLosers(dups []Duplicate) int {
	n := 0
	for _, d := range dups {
		n += len(d.Losers)
	}
	return n
}

func sortFindings(fs []Finding) []Finding {
	out := append([]Finding(nil), fs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Doc.Path < out[j].Doc.Path })
	return out
}

func valueOr(v string) string {
	if strings.TrimSpace(v) == "" {
		return "sin línea Estado/Prioridad"
	}
	return v
}

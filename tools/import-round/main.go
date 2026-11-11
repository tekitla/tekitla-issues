// import-round importa issues escritos en markdown a un proyecto de Tekitla
// con ImportIssue. Lee en solo lectura: no escribe nada en el repo de origen.
// Dos formatos (-formato):
//
//   - round (default): los markdown de documentacion/Issues de Round, al
//     proyecto RND (issue 025, §4).
//   - template-docs: un repo de docs con el formato de template-docs
//     (contenido/issues/<nnn>-<slug>/issue.md con frontmatter YAML), al
//     proyecto sugerido para el repo (TK para tekitla-docs, VR para el docs
//     de vrojas1015) o al de -project (issue 035).
//
// Se puede correr de nuevo: la clave de origen (número + slug) reconoce cada
// issue y una corrida sin cambios no crea ni cambia nada.
//
//	go run ./tools/import-round -user <uid> [-addr localhost:5000] [-create-project]
//	go run ./tools/import-round -dry-run            # sin servidor: solo lee y reporta
//	go run ./tools/import-round -formato template-docs -dir <repo de docs> -user <uid> [-project TK]
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

// sourceBaseURL es donde vive cada archivo de Round en GitLab (enlace de la
// descripción).
const sourceBaseURL = "https://gitlab.com/roel-education/documentacion/-/blob/main/Issues/"

// Valores de -formato.
const (
	formatRound        = "round"
	formatTemplateDocs = "template-docs"
)

// roundDefaultDir es la carpeta Issues de Round en la compu de Victor.
const roundDefaultDir = `C:\dev\round\docs\Issues`

type options struct {
	format        string
	dir           string
	repo          string
	addr          string
	user          string
	projectKey    string
	createProject bool
	dryRun        bool
	skipGitCheck  bool

	// client reemplaza la conexión gRPC (tests).
	client issuesv1.IssueServiceClient
}

func main() {
	var o options
	flag.StringVar(&o.format, "formato", formatRound, "formato del origen: round o template-docs")
	flag.StringVar(&o.dir, "dir", "", "round: carpeta Issues de documentacion (default "+roundDefaultDir+"); template-docs: raíz del repo de docs (obligatorio)")
	flag.StringVar(&o.repo, "repo", "", "template-docs: owner/repo de GitHub (default: el del remoto origin de -dir)")
	flag.StringVar(&o.addr, "addr", "localhost:5000", "dirección gRPC de tekitla-issues")
	flag.StringVar(&o.user, "user", "", "uid de Firebase de quien importa (x-user-id); obligatorio salvo con -dry-run")
	flag.StringVar(&o.projectKey, "project", "", "clave del proyecto destino (default: RND con round; con template-docs, la sugerida para el repo: TK o VR)")
	flag.BoolVar(&o.createProject, "create-project", false, "crear el proyecto si no existe")
	flag.BoolVar(&o.dryRun, "dry-run", false, "solo leer y reportar, sin llamar al servicio")
	flag.BoolVar(&o.skipGitCheck, "skip-git-check", false, "no verificar que la copia esté en main y al día")
	flag.Parse()

	if err := run(context.Background(), o, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "import-round:", err)
		os.Exit(1)
	}
}

// source es de dónde se importa: qué carpeta se lee, cómo se traduce cada
// issue y a qué proyecto va. El plan, el reporte y ImportIssue son los
// mismos para todos los formatos.
type source struct {
	format string
	// dir es la carpeta que se lee y cuya copia se verifica con git.
	dir string
	// Proyecto destino y, con -create-project, su nombre y descripción.
	key, name, description string
	read                   func() ([]*Doc, []Failure, error)
	translate              func(*Doc) translation
}

// resolveSource arma el origen según -formato y aplica los valores por
// defecto (carpeta, clave del proyecto).
func resolveSource(o options) (*source, error) {
	switch o.format {
	case "", formatRound:
		dir := o.dir
		if dir == "" {
			dir = roundDefaultDir
		}
		key := o.projectKey
		if key == "" {
			key = "RND"
		}
		return &source{
			format: formatRound, dir: dir, key: strings.ToUpper(strings.TrimSpace(key)), name: "Round",
			description: "Espejo de los issues de Round (documentacion/Issues), importados con tools/import-round.",
			read:        func() ([]*Doc, []Failure, error) { return readDocs(dir) },
			translate:   translateRound,
		}, nil
	case formatTemplateDocs:
		if o.dir == "" {
			return nil, errors.New("falta -dir (raíz del repo de docs, la que tiene contenido/issues)")
		}
		repo := strings.TrimSpace(o.repo)
		if repo == "" {
			remote, _ := git(o.dir, "remote", "get-url", "origin")
			repo = RepoFromRemote(remote)
		}
		// GitHub no distingue mayúsculas: en minúsculas, la descripción no
		// cambia según cómo se escribió -repo (CU-035-4).
		repo = strings.ToLower(repo)
		if repo == "" {
			return nil, fmt.Errorf("no sé de qué repo de GitHub es %s (sin remoto origin de GitHub): usa -repo owner/repo", o.dir)
		}
		known, isKnown := KnownRepos[repo]
		key := o.projectKey
		if key == "" {
			if !isKnown {
				return nil, fmt.Errorf("el repo %s no tiene clave sugerida: usa -project", repo)
			}
			key = known.Key
		}
		name := repo
		if isKnown {
			name = known.Name
		}
		issuesDir := filepath.Join(o.dir, IssuesSubdir)
		baseURL := "https://github.com/" + repo + "/blob/main/" + filepath.ToSlash(IssuesSubdir) + "/"
		return &source{
			format: formatTemplateDocs, dir: issuesDir, key: strings.ToUpper(strings.TrimSpace(key)), name: name,
			description: "Espejo de los issues de " + repo + " (contenido/issues), importados con tools/import-round -formato template-docs.",
			read:        func() ([]*Doc, []Failure, error) { return ReadTemplateDocs(issuesDir) },
			translate:   func(d *Doc) translation { return translateTemplate(d, repo, baseURL) },
		}, nil
	}
	return nil, fmt.Errorf("formato desconocido %q: round o template-docs", o.format)
}

// git corre git en dir, sin modificar nada, y devuelve la salida sin
// espacios en los extremos.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		err = fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(b)), err
}

func run(ctx context.Context, o options, out io.Writer) error {
	src, err := resolveSource(o)
	if err != nil {
		return err
	}
	if info, err := os.Stat(src.dir); err != nil || !info.IsDir() {
		if src.format == formatTemplateDocs {
			return fmt.Errorf("no existe %s: -dir tiene que ser la raíz del repo de docs (la que tiene contenido/issues)", src.dir)
		}
		return fmt.Errorf("no existe la carpeta %s", src.dir)
	}
	if !o.skipGitCheck {
		if err := checkGit(src.dir); err != nil {
			return err
		}
	}
	docs, broken, err := src.read()
	if err != nil {
		return err
	}
	rep := &Report{Format: src.format, Key: src.key, Broken: broken, Total: len(docs)}
	for _, d := range docs {
		if d.FolderNumber != 0 {
			rep.IDMismatch = append(rep.IDMismatch, d)
		}
	}
	if o.dryRun {
		plan := MakePlan(docs, Existing{})
		rep.Plan = plan
		for _, d := range plan.Import {
			rep.inspect(d, src.translate(d))
		}
		rep.Write(out, true)
		return nil
	}
	if o.user == "" {
		return errors.New("falta -user (uid de quien importa)")
	}

	client := o.client
	if client == nil {
		conn, err := grpc.NewClient(o.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("conectar a %s: %w", o.addr, err)
		}
		defer conn.Close()
		client = issuesv1.NewIssueServiceClient(conn)
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "x-user-id", o.user, "x-request-id", "import-round-"+time.Now().UTC().Format("20060102T150405Z"))

	project, err := findProject(ctx, client, src, o.createProject)
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
		req := rep.inspect(d, src.translate(d))
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

// checkGit verifica, sin modificar nada, que la copia esté en main, sin
// cambios en la carpeta que se lee y con el mismo commit que origin/main en
// el remoto (git ls-remote no toca la copia).
func checkGit(dir string) error {
	git := func(args ...string) (string, error) { return git(dir, args...) }
	branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("%s no es un repo git (usa -skip-git-check si es a propósito): %w", dir, err)
	}
	if branch != "main" {
		return fmt.Errorf("la copia está en %q, no en main: solo se importa main", branch)
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
		return fmt.Errorf("la copia no está al día con origin/main (local %s, remoto %s): hace falta un pull", short(head), short(remote))
	}
	return nil
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// readDocs lee los issues de Round de dir. Un archivo roto no frena a los
// demás: va en broken. Los .md que no son issues (README, _template) se
// ignoran.
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

func findProject(ctx context.Context, client issuesv1.IssueServiceClient, src *source, create bool) (*issuesv1.Project, error) {
	cursor := ""
	for {
		res, err := client.ListProjects(ctx, &issuesv1.ListProjectsRequest{Page: &issuesv1.PageRequest{Cursor: cursor, Limit: 100}})
		if err != nil {
			return nil, fmt.Errorf("ListProjects: %w", err)
		}
		for _, p := range res.GetProjects() {
			if strings.EqualFold(p.GetKey(), src.key) {
				return p, nil
			}
		}
		if !res.GetPage().GetHasNext() {
			break
		}
		cursor = res.GetPage().GetNextCursor()
	}
	if !create {
		return nil, fmt.Errorf("no existe el proyecto %s (usa -create-project para crearlo)", src.key)
	}
	res, err := client.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: src.key, Name: src.name, Description: src.description})
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

// translation es un issue traducido a Tekitla, con lo que no se reconoció.
type translation struct {
	State         issuesv1.IssueState
	StateKnown    bool
	Priority      issuesv1.Priority
	PriorityKnown bool
	// CreatedAt cero: sin fecha válida (va con la de hoy).
	CreatedAt   time.Time
	Labels      []string
	Description string
	Truncated   bool
	// LabelsDropped: había más de MaxLabelsPerIssue etiquetas.
	LabelsDropped bool
}

// translateRound traduce un issue de Round (§4 del issue 025).
func translateRound(d *Doc) translation {
	var t translation
	t.State, t.StateKnown = MapState(d.RawState)
	t.Priority, t.PriorityKnown = MapPriority(d.RawPriority)
	t.CreatedAt, _ = ParseDate(d.RawDate)
	if label := AreaLabel(d.RawArea); label != "" {
		t.Labels = []string{label}
	}
	t.Description, t.Truncated = Description(d, sourceBaseURL+d.File)
	return t
}

// translateTemplate traduce un issue de template-docs (tabla del issue 035).
func translateTemplate(d *Doc, repo, baseURL string) translation {
	var t translation
	var blocked bool
	t.State, blocked, t.StateKnown = MapTemplateState(d.RawState)
	t.Priority, t.PriorityKnown = MapTemplatePriority(d.RawPriority)
	t.CreatedAt, _ = ParseDate(d.RawDate)
	t.Labels, t.LabelsDropped = TemplateLabels(d.RawLabels, blocked)
	t.Description, t.Truncated = TemplateDescription(d, repo, baseURL)
	return t
}

// Report junta lo que pasó en la corrida.
type Report struct {
	// Format es el -formato de la corrida (para el título).
	Format string
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
	NoLabels        []*Doc
	Truncated       []*Doc
	// IDMismatch: template-docs con un id distinto del número de la carpeta
	// (se usa el id).
	IDMismatch     []*Doc
	TitleTruncated []*Doc
	LabelsDropped  []*Doc
}

// inspect arma el request de un issue ya traducido y anota lo que no se
// reconoció.
func (r *Report) inspect(d *Doc, t translation) *issuesv1.ImportIssueRequest {
	if !t.StateKnown {
		r.UnknownState = append(r.UnknownState, Finding{Doc: d, Value: d.RawState})
	}
	if !t.PriorityKnown {
		r.UnknownPriority = append(r.UnknownPriority, Finding{Doc: d, Value: d.RawPriority})
	}
	if t.Truncated {
		r.Truncated = append(r.Truncated, d)
	}
	if t.LabelsDropped {
		r.LabelsDropped = append(r.LabelsDropped, d)
	}
	title, cut := clampTitle(d.Title)
	if cut {
		r.TitleTruncated = append(r.TitleTruncated, d)
	}
	req := &issuesv1.ImportIssueRequest{
		SourceKey: d.SourceKey, Number: d.Number, Title: title, Description: t.Description,
		Priority: t.Priority, State: t.State, LabelNames: t.Labels,
	}
	if !t.CreatedAt.IsZero() {
		req.CreatedAt = timestamppb.New(t.CreatedAt)
	} else {
		r.NoDate = append(r.NoDate, d)
	}
	if len(t.Labels) == 0 {
		r.NoLabels = append(r.NoLabels, d)
	}
	return req
}

// Write imprime el reporte: cada caso con su ruta.
func (r *Report) Write(w io.Writer, dryRun bool) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }
	title := "== import-round"
	if r.Format == formatTemplateDocs {
		title += " -formato template-docs"
	}
	if dryRun {
		title += " (dry-run: no se llamó al servicio)"
	}
	p("%s", title)
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
	if section("Sin etiquetas", len(r.NoLabels)) {
		for _, d := range r.NoLabels {
			p("  %s", d.Path)
		}
	}
	if section("id distinto del número de la carpeta (se usa el id)", len(r.IDMismatch)) {
		for _, d := range r.IDMismatch {
			p("  %s-%d  %s (carpeta %d)", r.Key, d.Number, d.Path, d.FolderNumber)
		}
	}
	if section("Título recortado (más de 200 caracteres)", len(r.TitleTruncated)) {
		for _, d := range r.TitleTruncated {
			p("  %s", d.Path)
		}
	}
	if section("Etiquetas recortadas (más de 20: se quedan las primeras)", len(r.LabelsDropped)) {
		for _, d := range r.LabelsDropped {
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
		return "sin valor"
	}
	return v
}

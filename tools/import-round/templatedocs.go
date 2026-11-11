package main

// Lector del formato template-docs de dev-plantillas (issue 035): una carpeta
// contenido/issues/<nnn>-<slug>/ por issue, con issue.md y frontmatter YAML.
// El plan, el reporte y la llamada a ImportIssue son los mismos que con Round.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
	"gopkg.in/yaml.v3"
)

// IssuesSubdir es donde template-docs guarda los issues, desde la raíz del
// repo de docs.
var IssuesSubdir = filepath.Join("contenido", "issues")

// BlockedLabel es la etiqueta que lleva un issue con estado bloqueado (en
// Tekitla no hay estado «bloqueado»: va En curso con esta etiqueta).
const BlockedLabel = "bloqueado"

// knownRepo es un repo de docs con template-docs cuya clave sugerida se
// conoce (Victor, 2026-11-11).
type knownRepo struct {
	Key  string
	Name string
}

// KnownRepos: owner/repo de GitHub (en minúsculas) -> proyecto sugerido.
var KnownRepos = map[string]knownRepo{
	"tekitla/tekitla-docs": {Key: "TK", Name: "Tekitla"},
	"vrojas1015/docs":      {Key: "VR", Name: "vrojas1015"},
}

// githubRepo: owner/repo de un remoto de GitHub (https o ssh).
var githubRepo = regexp.MustCompile(`github\.com[:/]+([^/]+)/([^/]+?)(?:\.git)?/?$`)

// RepoFromRemote saca owner/repo de la URL del remoto ("" si no es GitHub).
func RepoFromRemote(url string) string {
	m := githubRepo.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// issueDir: <número>-<slug>.
var issueDir = regexp.MustCompile(`^(\d+)-(.+)$`)

// frontmatter son los campos de template-docs que usa el importador. Los
// demás (relacionados...) se ignoran.
type frontmatter struct {
	ID        *int32   `yaml:"id"`
	Titulo    string   `yaml:"titulo"`
	Estado    string   `yaml:"estado"`
	Tipo      string   `yaml:"tipo"`
	Prioridad string   `yaml:"prioridad"`
	Area      string   `yaml:"area"`
	Repos     []string `yaml:"repos"`
	Creado    string   `yaml:"creado"`
	Resuelto  string   `yaml:"resuelto"`
}

// errNoFrontmatter: el issue.md no empieza con un bloque --- ... ---.
var errNoFrontmatter = errors.New("sin frontmatter (--- al principio del archivo)")

// splitFrontmatter separa el YAML del cuerpo. El texto ya viene sin BOM y con
// \n. Abre con una línea --- y cierra con --- o ... (YAML acepta los dos),
// con o sin espacios al final.
func splitFrontmatter(text string) (yamlText, body string, err error) {
	lines := strings.Split(text, "\n")
	if strings.TrimRight(lines[0], " \t") != "---" {
		return "", "", errNoFrontmatter
	}
	for i := 1; i < len(lines); i++ {
		if l := strings.TrimRight(lines[i], " \t"); l == "---" || l == "..." {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), nil
		}
	}
	return "", "", errors.New("frontmatter sin cerrar (falta la línea ---)")
}

// ParseTemplateDoc lee el issue.md de la carpeta dir (p. ej.
// "030-mcp-de-lectura"). path es la ruta del archivo, para el reporte.
func ParseTemplateDoc(path, dir string, content []byte) (*Doc, error) {
	m := issueDir.FindStringSubmatch(dir)
	if m == nil {
		return nil, fmt.Errorf("la carpeta no es <número>-<slug>")
	}
	folderNumber, err := strconv.ParseInt(m[1], 10, 32)
	if err != nil || folderNumber <= 0 {
		return nil, fmt.Errorf("la carpeta no es <número>-<slug>")
	}
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("no es UTF-8 válido")
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	text = strings.TrimPrefix(text, string(rune(0xFEFF)))
	yamlText, body, err := splitFrontmatter(text)
	if err != nil {
		return nil, err
	}
	var fm frontmatter
	// Decode de un YAML vacío da io.EOF: frontmatter sin campos (falta id).
	if err := yaml.NewDecoder(strings.NewReader(yamlText)).Decode(&fm); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("frontmatter inválido: %v", err)
	}
	if fm.ID == nil {
		return nil, fmt.Errorf("el frontmatter no tiene id")
	}
	if *fm.ID <= 0 {
		return nil, fmt.Errorf("id %d: tiene que ser mayor que 0", *fm.ID)
	}

	// La clave de origen es el id (no la carpeta): el id es la identidad del
	// issue en template-docs y no se renumera, y así renombrar el slug de la
	// carpeta sigue actualizando el mismo issue.
	doc := &Doc{
		Path: path, File: "issue.md", Dir: dir, SourceKey: strconv.Itoa(int(*fm.ID)),
		Number: *fm.ID, Slug: m[2],
		Title:    strings.TrimSpace(fm.Titulo),
		RawState: fm.Estado, RawPriority: fm.Prioridad, RawDate: fm.Creado,
		RawResolved: strings.TrimSpace(fm.Resuelto),
	}
	if int32(folderNumber) != doc.Number {
		doc.FolderNumber = int32(folderNumber)
	}
	doc.RawLabels = append([]string{fm.Tipo, fm.Area}, fm.Repos...)

	// El cuerpo, sin el título (# ...) si repite titulo (o si no hay titulo:
	// entonces es el título). Un # distinto de titulo se queda en el cuerpo.
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "# ") {
			heading := strings.TrimSpace(line[2:])
			if doc.Title == "" {
				doc.Title = heading
			}
			if heading == doc.Title {
				lines = append(lines[:i:i], lines[i+1:]...)
			}
			break
		}
		if strings.TrimSpace(line) != "" {
			break // el título, si está, es lo primero del cuerpo
		}
	}
	if doc.Title == "" {
		doc.Title = doc.Slug
	}
	doc.Body = strings.TrimSpace(strings.Join(lines, "\n"))
	return doc, nil
}

// ReadTemplateDocs lee los issues de <repo>/contenido/issues. Un issue roto
// (sin issue.md, sin frontmatter, YAML inválido) no frena a los demás: va en
// broken. ADRs, runbooks e índices no se leen: solo las carpetas
// <número>-<slug>.
func ReadTemplateDocs(issuesDir string) ([]*Doc, []Failure, error) {
	entries, err := os.ReadDir(issuesDir)
	if err != nil {
		return nil, nil, fmt.Errorf("leer %s: %w", issuesDir, err)
	}
	var docs []*Doc
	var broken []Failure
	for _, e := range entries {
		if !e.IsDir() || !issueDir.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(issuesDir, e.Name())
		path := filepath.Join(dir, "issue.md")
		fail := func(msg string) {
			broken = append(broken, Failure{Doc: &Doc{Path: path, File: "issue.md", Dir: e.Name()}, Err: msg})
		}
		content, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fail("la carpeta no tiene issue.md")
			} else {
				fail(err.Error())
			}
			continue
		}
		doc, err := ParseTemplateDoc(path, e.Name(), content)
		if err != nil {
			fail(err.Error())
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "plan.md")); err == nil {
			doc.HasPlan = true
		}
		docs = append(docs, doc)
	}
	return docs, broken, nil
}

// MapTemplateState mapea el estado de template-docs. blocked = true para
// bloqueado (va En curso con la etiqueta BlockedLabel). known = false si el
// valor no está en la tabla o falta: va como TODO y al reporte.
func MapTemplateState(raw string) (state issuesv1.IssueState, blocked, known bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "abierto":
		return issuesv1.IssueState_ISSUE_STATE_TODO, false, true
	case "en-curso":
		return issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS, false, true
	case "bloqueado":
		return issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS, true, true
	case "resuelto":
		return issuesv1.IssueState_ISSUE_STATE_DONE, false, true
	case "descartado":
		return issuesv1.IssueState_ISSUE_STATE_CANCELED, false, true
	}
	return issuesv1.IssueState_ISSUE_STATE_TODO, false, false
}

// MapTemplatePriority mapea alta · media · baja. known = false si falta o no
// se reconoce: va como MEDIUM y al reporte.
func MapTemplatePriority(raw string) (priority issuesv1.Priority, known bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "alta":
		return issuesv1.Priority_PRIORITY_HIGH, true
	case "media":
		return issuesv1.Priority_PRIORITY_MEDIUM, true
	case "baja":
		return issuesv1.Priority_PRIORITY_LOW, true
	}
	return issuesv1.Priority_PRIORITY_MEDIUM, false
}

// MaxLabelsPerIssue es el máximo de etiquetas de un issue en Tekitla.
const MaxLabelsPerIssue = 20

// TemplateLabels arma las etiquetas: tipo, BlockedLabel si blocked, area y
// repos (en ese orden). Normalizadas (normalizeLabel), sin vacías ni
// repetidas (area suele repetir uno de los repos) y a MaxLabelsPerIssue como
// máximo: las que sobran (repos del final) se pierden y dropped = true.
func TemplateLabels(raw []string, blocked bool) (labels []string, dropped bool) {
	if blocked && len(raw) > 0 {
		raw = append([]string{raw[0], BlockedLabel}, raw[1:]...)
	} else if blocked {
		raw = []string{BlockedLabel}
	}
	seen := map[string]bool{}
	for _, r := range raw {
		s := normalizeLabel(r)
		if s == "" || s == "null" || seen[s] {
			continue
		}
		if len(labels) == MaxLabelsPerIssue {
			return labels, true
		}
		seen[s] = true
		labels = append(labels, s)
	}
	return labels, false
}

// TemplateDescription arma la descripción: el cuerpo y un pie con el enlace
// al issue.md en GitHub (rama main), el plan.md si lo hay y la fecha de
// resuelto (el contrato no tiene dónde guardarla). baseURL es
// https://github.com/<owner>/<repo>/blob/main/contenido/issues/.
func TemplateDescription(doc *Doc, repo, baseURL string) (desc string, truncated bool) {
	issueURL := baseURL + doc.Dir + "/issue.md"
	footer := fmt.Sprintf("Importado de `%s`: [`contenido/issues/%s/issue.md`](%s) (rama `main`).", repo, doc.Dir, issueURL)
	if doc.HasPlan {
		footer += fmt.Sprintf("\nPlan: [`plan.md`](%s).", baseURL+doc.Dir+"/plan.md")
	}
	if t, ok := ParseDate(doc.RawResolved); ok {
		footer += fmt.Sprintf("\nResuelto: %s.", t.Format("2006-01-02"))
	}
	return describe(doc.Body, footer)
}

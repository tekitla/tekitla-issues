package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
	"golang.org/x/text/unicode/norm"
)

// Doc es un issue de Round leído de su markdown.
type Doc struct {
	// Path es la ruta del archivo (para el reporte).
	Path string
	// File es el nombre del archivo, p. ej. "236-claude-md-en-capas.md".
	File string
	// SourceKey es el nombre sin .md: número + slug.
	SourceKey string
	Number    int32
	Slug      string
	Title     string
	// Campos de la cabecera tal como vienen ("" si no están).
	RawState    string
	RawPriority string
	RawArea     string
	RawDate     string
	// Body es el markdown sin la línea del título.
	Body string
}

// fileName: número + slug. Los demás .md (README, _template) no son issues.
var fileName = regexp.MustCompile(`^(\d+)-(.+)\.md$`)

// ParseName reconoce un archivo de issue por su nombre. ok = false si no lo
// es (README.md, _template.md...).
func ParseName(file string) (number int32, slug string, ok bool) {
	m := fileName.FindStringSubmatch(file)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.ParseInt(m[1], 10, 32)
	if err != nil || n <= 0 {
		return 0, "", false
	}
	return int32(n), m[2], true
}

// headerField: "> **Campo:** valor" o "**Campo:** valor" (las dos plantillas
// que conviven en Round).
var headerField = regexp.MustCompile(`^\s*(?:>\s*)?\*\*([^*]+?):\*\*\s*(.*?)\s*$`)

// titlePrefix: "[236] ", "Issue 005 — ", "214 · ".
var titlePrefix = regexp.MustCompile(`^(?:\[\d+\]\s*|Issue\s+\d+\s*[—–-]\s*|\d+\s*[·.:—–-]\s*)`)

// ParseDoc lee un issue. La cabecera son los campos en negrita antes de la
// primera sección (## ); de cada campo vale la primera aparición.
func ParseDoc(path, file string, content []byte) (*Doc, error) {
	number, slug, ok := ParseName(file)
	if !ok {
		return nil, fmt.Errorf("el nombre no es <número>-<slug>.md")
	}
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("no es UTF-8 válido")
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	text = strings.TrimPrefix(text, "\ufeff")
	doc := &Doc{Path: path, File: file, SourceKey: strings.TrimSuffix(file, ".md"), Number: number, Slug: slug}

	lines := strings.Split(text, "\n")
	titleLine := -1
	fields := map[string]string{}
	for i, line := range lines {
		if titleLine < 0 && strings.HasPrefix(line, "# ") {
			titleLine = i
			doc.Title = strings.TrimSpace(titlePrefix.ReplaceAllString(strings.TrimSpace(line[2:]), ""))
			continue
		}
		if strings.HasPrefix(line, "## ") {
			break
		}
		if m := headerField.FindStringSubmatch(line); m != nil {
			key := fieldKey(m[1])
			if _, seen := fields[key]; !seen {
				fields[key] = m[2]
			}
		}
	}
	if doc.Title == "" {
		doc.Title = slug
	}
	doc.RawState = fields["estado"]
	doc.RawPriority = fields["prioridad"]
	doc.RawArea = firstOf(fields, "area principal", "area")
	doc.RawDate = firstOf(fields, "fecha detectado", "detectado", "fecha")
	if titleLine >= 0 {
		lines = append(lines[:titleLine:titleLine], lines[titleLine+1:]...)
	}
	doc.Body = strings.TrimSpace(strings.Join(lines, "\n"))
	return doc, nil
}

func firstOf(fields map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := fields[k]; ok {
			return v
		}
	}
	return ""
}

// fieldKey normaliza el nombre de un campo: minúsculas y sin acentos.
func fieldKey(name string) string {
	return strings.TrimSpace(foldAccents(strings.ToLower(name)))
}

func foldAccents(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// leadingToken es el valor «de verdad» de un campo: el texto antes del
// primer «—»; de ahí, lo primero entre comillas invertidas si empieza con
// una, o la primera palabra. Sin negritas ni comillas, en minúsculas.
func leadingToken(raw string) string {
	s := raw
	if i := strings.Index(s, "—"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(strings.ReplaceAll(s, "**", ""))
	if strings.HasPrefix(s, "`") {
		if end := strings.Index(s[1:], "`"); end >= 0 {
			s = s[1 : end+1]
		}
	} else if f := strings.Fields(s); len(f) > 0 {
		s = f[0]
	}
	return strings.ToLower(strings.Trim(strings.TrimSpace(s), "`*_.,;:()"))
}

// MapState mapea el Estado de Round (§4 del issue 025). known = false si el
// valor no está en la tabla o falta: va como TODO y al reporte.
func MapState(raw string) (state issuesv1.IssueState, known bool) {
	v := leadingToken(raw)
	switch {
	case v == "open" || v == "pending-validation":
		return issuesv1.IssueState_ISSUE_STATE_TODO, true
	case v == "in-progress" || v == "parcial" || v == "blocked" || strings.HasPrefix(v, "partial"):
		return issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS, true
	case v == "resolved" || v == "resuelto" || v == "done" || v == "closed" || strings.HasPrefix(v, "delivered"):
		return issuesv1.IssueState_ISSUE_STATE_DONE, true
	case v == "won't-do" || v == "won’t-do" || v == "superseded":
		return issuesv1.IssueState_ISSUE_STATE_CANCELED, true
	}
	return issuesv1.IssueState_ISSUE_STATE_TODO, false
}

// MapPriority mapea la primera palabra de la Prioridad. known = false si
// falta o no se reconoce: va como MEDIUM y al reporte. «crítica» va como
// URGENT (Round la usa en 4 issues y Tekitla la tiene).
func MapPriority(raw string) (priority issuesv1.Priority, known bool) {
	v := foldAccents(leadingToken(raw))
	if i := strings.IndexFunc(v, func(r rune) bool { return !unicode.IsLetter(r) }); i >= 0 {
		v = v[:i] // "media-alta" -> "media"
	}
	switch v {
	case "alta":
		return issuesv1.Priority_PRIORITY_HIGH, true
	case "media":
		return issuesv1.Priority_PRIORITY_MEDIUM, true
	case "baja":
		return issuesv1.Priority_PRIORITY_LOW, true
	case "critica", "urgente":
		return issuesv1.Priority_PRIORITY_URGENT, true
	}
	return issuesv1.Priority_PRIORITY_MEDIUM, false
}

// isoDate: la primera fecha AAAA-MM-DD del campo.
var isoDate = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

// ParseDate lee la fecha de alta. ok = false si no hay una válida.
func ParseDate(raw string) (time.Time, bool) {
	m := isoDate.FindString(raw)
	if m == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", m)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// LabelMaxLen es el largo máximo de una etiqueta en Tekitla.
const LabelMaxLen = 40

// AreaLabel es la etiqueta del Área principal: si empieza entre comillas
// invertidas, lo que hay entre ellas; si no, el texto antes de «—» o «(». En
// minúsculas y a LabelMaxLen runas como máximo. "" si no hay.
func AreaLabel(raw string) string {
	s := strings.TrimSpace(strings.ReplaceAll(raw, "**", ""))
	if strings.HasPrefix(s, "`") {
		if end := strings.Index(s[1:], "`"); end > 0 {
			s = s[1 : end+1]
		}
	} else if i := strings.IndexAny(s, "—("); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), "`"))
	if utf8.RuneCountInString(s) > LabelMaxLen {
		s = string([]rune(s)[:LabelMaxLen])
	}
	return strings.TrimSpace(s)
}

// DescriptionMaxLen es el largo máximo de una descripción en Tekitla.
const DescriptionMaxLen = 20000

// Description arma la descripción: el cuerpo del markdown y un enlace al
// archivo de origen. Si no entra, recorta el cuerpo y lo avisa en el texto
// (truncated = true).
func Description(doc *Doc, sourceURL string) (desc string, truncated bool) {
	footer := fmt.Sprintf("\n\n---\n\nImportado de Round: [`Issues/%s`](%s) (rama `main` de `documentacion`).", doc.File, sourceURL)
	body := doc.Body
	if utf8.RuneCountInString(body)+utf8.RuneCountInString(footer) <= DescriptionMaxLen {
		return body + footer, false
	}
	note := fmt.Sprintf("\n\n_(Recortado: el original tiene %d caracteres. El texto completo está en el archivo de origen.)_", utf8.RuneCountInString(body))
	keep := DescriptionMaxLen - utf8.RuneCountInString(footer) - utf8.RuneCountInString(note)
	return strings.TrimSpace(string([]rune(body)[:keep])) + note + footer, true
}

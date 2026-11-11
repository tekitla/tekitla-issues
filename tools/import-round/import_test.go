package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

const quoteHeader = `# [236] CLAUDE.md en capas y workspace de Round fuera de OneDrive

> **Estado:** ` + "`in-progress`" + `
> **Área principal:** ` + "`infra`" + `
> **Áreas secundarias:** todos los repos
> **Prioridad:** ` + "`alta`" + `
> **Fecha detectado:** 2026-11-09
> **Fecha resuelto:** _(vacío si open)_

---

## Descripción

> **Estado:** ` + "`resolved`" + ` (esto es del cuerpo, no de la cabecera)
Texto.
`

const boldHeader = `# Issue 005 — Tests de componentes fallan

**Área:** ` + "`front/<admin>`" + ` — vitest / test-setup
**Prioridad:** baja
**Detectado:** 2026-04-22
**Estado:** ` + "`open`" + `

---

## Descripción
`

// CU-025-11: Estado in-progress y Prioridad alta -> IN_PROGRESS y HIGH.
func TestParseDoc_QuoteHeader(t *testing.T) {
	doc, err := ParseDoc("/r/236-claude-md-en-capas.md", "236-claude-md-en-capas.md", []byte(strings.ReplaceAll(quoteHeader, "\n", "\r\n")))
	if err != nil {
		t.Fatalf("ParseDoc: %v", err)
	}
	if doc.Number != 236 || doc.SourceKey != "236-claude-md-en-capas" || doc.Title != "CLAUDE.md en capas y workspace de Round fuera de OneDrive" {
		t.Errorf("doc: %+v", doc)
	}
	if st, ok := MapState(doc.RawState); !ok || st != issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS {
		t.Errorf("state %q -> %v %v", doc.RawState, st, ok)
	}
	if p, ok := MapPriority(doc.RawPriority); !ok || p != issuesv1.Priority_PRIORITY_HIGH {
		t.Errorf("priority %q -> %v %v", doc.RawPriority, p, ok)
	}
	if AreaLabel(doc.RawArea) != "infra" {
		t.Errorf("area %q", doc.RawArea)
	}
	if d, ok := ParseDate(doc.RawDate); !ok || d.Format("2006-01-02") != "2026-11-09" {
		t.Errorf("date %q", doc.RawDate)
	}
	if strings.Contains(doc.Body, "# [236]") || !strings.HasPrefix(doc.Body, "> **Estado:**") {
		t.Errorf("body keeps the header and drops the title: %q", doc.Body[:40])
	}
}

func TestParseDoc_BoldHeader(t *testing.T) {
	doc, err := ParseDoc("/r/005-x.md", "005-x.md", []byte(boldHeader))
	if err != nil {
		t.Fatalf("ParseDoc: %v", err)
	}
	if doc.Number != 5 || doc.Title != "Tests de componentes fallan" || AreaLabel(doc.RawArea) != "front/<admin>" {
		t.Errorf("doc: %+v (area %q)", doc, AreaLabel(doc.RawArea))
	}
	if st, ok := MapState(doc.RawState); !ok || st != issuesv1.IssueState_ISSUE_STATE_TODO {
		t.Errorf("state %q", doc.RawState)
	}
	if p, _ := MapPriority(doc.RawPriority); p != issuesv1.Priority_PRIORITY_LOW {
		t.Errorf("priority %q", doc.RawPriority)
	}
	if _, ok := ParseDate(doc.RawDate); !ok {
		t.Errorf("date %q", doc.RawDate)
	}
	if _, _, ok := ParseName("README.md"); ok {
		t.Errorf("README is not an issue")
	}
	if _, _, ok := ParseName("_template.md"); ok {
		t.Errorf("_template is not an issue")
	}
	if _, err := ParseDoc("/r/007-x.md", "007-x.md", []byte{0xff, 0xfe}); err == nil {
		t.Errorf("invalid UTF-8 must fail")
	}
}

func TestMapState(t *testing.T) {
	cases := map[string]issuesv1.IssueState{
		"`open`":               issuesv1.IssueState_ISSUE_STATE_TODO,
		"`pending-validation`": issuesv1.IssueState_ISSUE_STATE_TODO,
		"`in-progress` (round-protos done en v0.31.0)": issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS,
		"partial":                 issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS,
		"`partially-shipped`":     issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS,
		"parcial":                 issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS,
		"`blocked`":               issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS,
		"`resolved` — 2026-04-22": issuesv1.IssueState_ISSUE_STATE_DONE,
		"`resolved` — won't-do (cerrado 2026-05-31)": issuesv1.IssueState_ISSUE_STATE_DONE,
		"resuelto — desplegado en QA":                issuesv1.IssueState_ISSUE_STATE_DONE,
		"`resolved` en las tres pantallas":           issuesv1.IssueState_ISSUE_STATE_DONE,
		"**done**":                                   issuesv1.IssueState_ISSUE_STATE_DONE,
		"closed":                                     issuesv1.IssueState_ISSUE_STATE_DONE,
		"`delivered-qa`":                             issuesv1.IssueState_ISSUE_STATE_DONE,
		"`won't-do`":                                 issuesv1.IssueState_ISSUE_STATE_CANCELED,
		"Superseded":                                 issuesv1.IssueState_ISSUE_STATE_CANCELED,
		"`open` | `in-progress` | `resolved`":        issuesv1.IssueState_ISSUE_STATE_TODO,
		// Decididos por Victor el 2026-11-11 (issue 036).
		"abierto":   issuesv1.IssueState_ISSUE_STATE_TODO,
		"`abierto`": issuesv1.IssueState_ISSUE_STATE_TODO,
		"`in-review` — school-service ya devuelve el perfil (MR en borrador)": issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS,
		"`front arreglado` · falta el fix de fondo":                           issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS,
		"`resuelto (pendiente de deploy a QA)`":                               issuesv1.IssueState_ISSUE_STATE_DONE,
		"`resuelto en código` — mergeado, falta confirmar el despliegue":      issuesv1.IssueState_ISSUE_STATE_DONE,
		"✅ `resuelto y verificado en producción` (2026-08-27)":                issuesv1.IssueState_ISSUE_STATE_DONE,
	}
	for raw, want := range cases {
		if got, ok := MapState(raw); !ok || got != want {
			t.Errorf("MapState(%q) = %v %v, want %v", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "   ", "`archivado`", "en revisión", "propuesta, sin implementar", "✅"} {
		if got, ok := MapState(raw); ok || got != issuesv1.IssueState_ISSUE_STATE_TODO {
			t.Errorf("MapState(%q) = %v %v, want TODO unknown", raw, got, ok)
		}
	}
}

func TestMapPriority(t *testing.T) {
	cases := map[string]issuesv1.Priority{
		"`alta`":                          issuesv1.Priority_PRIORITY_HIGH,
		"**alta**":                        issuesv1.Priority_PRIORITY_HIGH,
		"media — hay pantalla y funciona": issuesv1.Priority_PRIORITY_MEDIUM,
		"media-alta":                      issuesv1.Priority_PRIORITY_MEDIUM,
		"Baja":                            issuesv1.Priority_PRIORITY_LOW,
		"`critica`":                       issuesv1.Priority_PRIORITY_URGENT,
		"crítica":                         issuesv1.Priority_PRIORITY_URGENT,
	}
	for raw, want := range cases {
		if got, ok := MapPriority(raw); !ok || got != want {
			t.Errorf("MapPriority(%q) = %v %v, want %v", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "p1"} {
		if got, ok := MapPriority(raw); ok || got != issuesv1.Priority_PRIORITY_MEDIUM {
			t.Errorf("MapPriority(%q) = %v %v, want MEDIUM unknown", raw, got, ok)
		}
	}
}

func TestAreaLabel(t *testing.T) {
	cases := map[string]string{
		"`round-protos` → `micro/school`":      "round-protos",
		"`front/Admin-Round`":                  "front/admin-round",
		"correo saliente (Mailgun `mg.round`)": "correo saliente",
		"correo saliente — algo":               "correo saliente",
		"":                                     "",
		"`" + strings.Repeat("a", 50) + "`":    strings.Repeat("a", 40),
	}
	for raw, want := range cases {
		if got := AreaLabel(raw); got != want {
			t.Errorf("AreaLabel(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestDescription(t *testing.T) {
	doc := &Doc{File: "001-a.md", Body: "Cuerpo"}
	desc, truncated := Description(doc, "https://x/001-a.md")
	if truncated || !strings.HasPrefix(desc, "Cuerpo") || !strings.Contains(desc, "(https://x/001-a.md)") {
		t.Errorf("short: %q", desc)
	}
	doc.Body = strings.Repeat("ñ", 30000)
	desc, truncated = Description(doc, "https://x/001-a.md")
	if !truncated || utf8.RuneCountInString(desc) > DescriptionMaxLen || !strings.Contains(desc, "30000 caracteres") || !strings.HasSuffix(desc, "de `documentacion`).") {
		t.Errorf("long: truncated %v, len %d", truncated, utf8.RuneCountInString(desc))
	}
}

func docs(keys ...string) []*Doc {
	out := make([]*Doc, len(keys))
	for i, k := range keys {
		n, slug, ok := ParseName(k + ".md")
		if !ok {
			panic(k)
		}
		out[i] = &Doc{Path: "/r/" + k + ".md", File: k + ".md", SourceKey: k, Number: n, Slug: slug}
	}
	return out
}

func importedKeys(p Plan) []string {
	var out []string
	for _, d := range p.Import {
		out = append(out, d.SourceKey)
	}
	return out
}

// CU-025-13: RND-3 queda ligado a 003-a sin importar el orden de lectura;
// 003-b queda afuera y el resto se importa. Después 003-b pasa a 243-b: se
// crea RND-243 y RND-3 no cambia.
func TestMakePlan_Duplicates(t *testing.T) {
	for _, order := range [][]string{{"003-a", "003-b", "004-c"}, {"003-b", "004-c", "003-a"}} {
		plan := MakePlan(docs(order...), Existing{})
		if got := strings.Join(importedKeys(plan), ","); got != "003-a,004-c" {
			t.Errorf("order %v: imported %s", order, got)
		}
		if len(plan.Duplicates) != 1 || plan.Duplicates[0].Winner.SourceKey != "003-a" || plan.Duplicates[0].Losers[0].SourceKey != "003-b" {
			t.Errorf("order %v: duplicates %+v", order, plan.Duplicates)
		}
	}

	// Ya importado: se queda el que tiene el número, aunque otro ordene antes.
	plan := MakePlan(docs("003-a", "003-b"), Existing{3: "003-b"})
	if got := strings.Join(importedKeys(plan), ","); got != "003-b" || plan.Duplicates[0].Losers[0].SourceKey != "003-a" {
		t.Errorf("already imported wins: %s %+v", got, plan.Duplicates)
	}

	// 003-b se renumera a 243-b.
	plan = MakePlan(docs("003-a", "243-b", "004-c"), Existing{3: "003-a", 4: "004-c"})
	if got := strings.Join(importedKeys(plan), ","); got != "003-a,004-c,243-b" || len(plan.Duplicates) != 0 || len(plan.Orphans) != 0 {
		t.Errorf("renumbered: %s dups %v orphans %v", got, plan.Duplicates, plan.Orphans)
	}

	// Round renombra el que tenía el número: el viejo queda «sin archivo» y
	// el nuevo no puede quedarse con RND-3.
	plan = MakePlan(docs("003-a-nuevo"), Existing{3: "003-a"})
	if len(plan.Import) != 0 || len(plan.Orphans) != 1 || plan.Orphans[0].SourceKey != "003-a" ||
		len(plan.Duplicates) != 1 || plan.Duplicates[0].Winner != nil || plan.Duplicates[0].HolderKey != "003-a" {
		t.Errorf("renamed holder: %+v", plan)
	}

	// Slug por bytes (UTF-8), no por mayúsculas ni locale.
	plan = MakePlan(docs("010-b", "010-B", "010-á"), Existing{})
	if plan.Import[0].SourceKey != "010-B" {
		t.Errorf("byte order: got %s", plan.Import[0].SourceKey)
	}
}

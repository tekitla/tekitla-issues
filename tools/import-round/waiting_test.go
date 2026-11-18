package main

// Issue 039, CU-039-8: «Espera:» → etiqueta espera/<nombre>. Datos inventados.

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestParseDoc_Waiting(t *testing.T) {
	quoted := "# [901] Algo\n\n> **Estado:** `open`\n> **Perfil:** escuela\n> **Espera:** Rosa Núñez, beto\n\n## Descripción\n"
	bare := "# 902 · Otra\n\n**Estado:** open\n**Espera:** rosa — revisa el texto\n\n## Descripción\n"
	d1, err := ParseDoc("/r/901-algo.md", "901-algo.md", []byte(quoted))
	if err != nil {
		t.Fatal(err)
	}
	if got := categoryLabels(d1); !reflect.DeepEqual(got, []string{"perfil/escuela", "espera/rosa-nunez", "espera/beto"}) {
		t.Errorf("quoted: %v", got)
	}
	d2, err := ParseDoc("/r/902-otra.md", "902-otra.md", []byte(bare))
	if err != nil {
		t.Fatal(err)
	}
	if got := categoryLabels(d2); !reflect.DeepEqual(got, []string{"espera/rosa"}) {
		t.Errorf("bare: %v", got)
	}

	content := "---\nid: 7\ntitulo: X\nestado: abierto\nespera: [Rosa]\n---\n# X\n"
	d3, err := ParseTemplateDoc("/d/007-x/issue.md", "007-x", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if tr := translateTemplate(d3, "o/r", "https://x/"); !slices.Contains(tr.Labels, "espera/rosa") {
		t.Errorf("template-docs labels %v", tr.Labels)
	}
}

// Dos corridas sin cambios no escriben; si la línea desaparece, la etiqueta
// se quita (el archivo manda) y las demás se quedan.
func TestRunRound_Waiting(t *testing.T) {
	dir := writeRound(t, map[string]string{
		"901-espera.md": roundIssue("Espera", "> **Perfil:** familia\n> **Espera:** Rosa\n"),
	})
	f := newRoundFake()
	runRound(t, f, dir, "")
	if got := f.labels("901-espera"); !reflect.DeepEqual(got, []string{"web", "perfil/familia", "espera/rosa"}) {
		t.Fatalf("labels %v", got)
	}
	out := runRound(t, f, dir, "")
	if !strings.Contains(out, "sin cambios: 1") {
		t.Errorf("second run must not write:\n%s", out)
	}

	if err := os.WriteFile(filepath.Join(dir, "901-espera.md"), []byte(roundIssue("Espera", "> **Perfil:** familia\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	runRound(t, f, dir, "")
	if got := f.labels("901-espera"); !reflect.DeepEqual(got, []string{"web", "perfil/familia"}) {
		t.Errorf("after removing Espera: %v", got)
	}
}

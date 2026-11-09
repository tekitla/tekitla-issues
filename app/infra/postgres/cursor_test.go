package postgres

import (
	"encoding/base64"
	"testing"
)

const cursorTestID = "0b7e6a8e-6c1f-4c8e-9a51-2f3c4d5e6f70"

func TestProjectCursorRoundTrip(t *testing.T) {
	in := projectCursor{Name: "Tekitla | ñandú \"x\"", ID: cursorTestID}

	out, err := decodeProjectCursor(encodeCursor(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != in {
		t.Errorf("round trip: got %+v, want %+v", out, in)
	}
}

func TestIssueCursorRoundTrip(t *testing.T) {
	in := issueCursor{Rank: "a0V", UpdatedMicros: 1760000000000000, Priority: 3, Filter: "abc"}

	out, err := decodeIssueCursor(encodeCursor(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != in {
		t.Errorf("round trip: got %+v, want %+v", out, in)
	}
}

func TestDecodeCursor_Invalid(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	badProject := []string{
		"%%%",                    // no es base64
		b64("nojson"),            // no es JSON
		b64(`{"n":"a","i":"x"}`), // id no UUID
		b64(`{"n":"a"}`),         // sin id
		b64(`{"n":"a","i":"` + cursorTestID + `","z":1}`), // campo desconocido
	}
	for _, s := range badProject {
		if _, err := decodeProjectCursor(s); err == nil {
			t.Errorf("decodeProjectCursor(%q): expected error", s)
		}
	}

	badIssue := []string{
		"%%%",
		b64(`{"r":""}`),    // sin rank
		b64(`{"r":1}`),     // rank no es string
		b64(`{"r":"~~~"}`), // rank fuera del formato
		b64(`{"r":"a00"}`), // rank terminado en 0
		b64(`{"r":"a0","i":"` + cursorTestID + `"}`), // campo desconocido
		b64(`{"r":"a0"}`), // sin huella de filtros (v0.3)
		b64(`{"p":2,"t":1,"i":"` + cursorTestID + `"}`),          // cursor del orden por prioridad (v0.1)
		encodeCursor(projectCursor{Name: "a", ID: cursorTestID}), // cursor de otro listado
	}
	for _, s := range badIssue {
		if _, err := decodeIssueCursor(s); err == nil {
			t.Errorf("decodeIssueCursor(%q): expected error", s)
		}
	}
}

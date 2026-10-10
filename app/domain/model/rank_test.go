package model_test

import (
	"testing"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Casos tomados de los tests del paquete fractional-indexing de rocicorp, del
// que rank.go es un port.
func TestRankBetween(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "", "a0"},
		{"", "a0", "Zz"},
		{"", "Zz", "Zy"},
		{"a0", "", "a1"},
		{"a1", "", "a2"},
		{"a0", "a1", "a0V"},
		{"a1", "a2", "a1V"},
		{"a0V", "a1", "a0l"},
		{"Zz", "a0", "ZzV"},
		{"Zz", "a1", "a0"},
		{"", "Y00", "Xzzz"},
		{"bzz", "", "c000"},
		{"a0", "a0V", "a0G"},
		{"a0", "a0G", "a08"},
		{"b125", "b129", "b127"},
		{"a0", "a1V", "a1"},
		{"Zz", "a01", "a0"},
		{"", "a0V", "a0"},
		{"", "b999", "b99"},
		{"", "A00000000000000000000000000V", "A00000000000000000000000000G"},
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzy", "", "zzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzz", "", "zzzzzzzzzzzzzzzzzzzzzzzzzzzV"},
	}
	for _, c := range cases {
		got, err := model.RankBetween(c.a, c.b)
		if err != nil {
			t.Errorf("RankBetween(%q, %q): unexpected error %v", c.a, c.b, err)
			continue
		}
		if got != c.want {
			t.Errorf("RankBetween(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestRankBetween_Invalid(t *testing.T) {
	cases := []struct{ a, b string }{
		{"a00", ""},                         // fracción terminada en 0
		{"a00", "a1"},                       // idem
		{"0", "1"},                          // encabezado inválido
		{"a1", "a0"},                        // fuera de orden
		{"a0", "a0"},                        // iguales
		{"a", ""},                           // parte entera incompleta
		{"a0-", ""},                         // carácter fuera del alfabeto
		{"", "A00000000000000000000000000"}, // la parte entera más chica no es clave
	}
	for _, c := range cases {
		if _, err := model.RankBetween(c.a, c.b); !domainerrors.IsValidation(err) {
			t.Errorf("RankBetween(%q, %q): want ValidationError, got %v", c.a, c.b, err)
		}
	}
}

// Agregar al final muchas veces produce claves estrictamente crecientes y
// cortas (el largo crece con el log del número de issues).
func TestRankAfter_Sequence(t *testing.T) {
	const n = 10000
	prev := ""
	for i := 0; i < n; i++ {
		next, err := model.RankAfter(prev)
		if err != nil {
			t.Fatalf("RankAfter(%q): %v", prev, err)
		}
		if prev != "" && next <= prev {
			t.Fatalf("RankAfter(%q) = %q, not greater", prev, next)
		}
		prev = next
	}
	if len(prev) > 4 {
		t.Errorf("after %d issues the rank is %q (%d chars), want <= 4", n, prev, len(prev))
	}
}

// Insertar siempre entre las dos primeras claves sigue produciendo claves
// ordenadas (lo que hará reordenar en la rebanada 3).
func TestRankBetween_Repeated(t *testing.T) {
	lo, hi := "a0", "a1"
	for i := 0; i < 200; i++ {
		mid, err := model.RankBetween(lo, hi)
		if err != nil {
			t.Fatalf("RankBetween(%q, %q): %v", lo, hi, err)
		}
		if !(lo < mid && mid < hi) {
			t.Fatalf("RankBetween(%q, %q) = %q, not between", lo, hi, mid)
		}
		if i%2 == 0 {
			hi = mid
		} else {
			lo = mid
		}
	}
}

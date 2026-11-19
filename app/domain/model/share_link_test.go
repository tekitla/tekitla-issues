package model_test

import (
	"strings"
	"testing"
	"time"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Mismos casos que waitingNote del panel (plan.logic.spec.ts, CU-039-10).
func TestWaitingNote(t *testing.T) {
	quoted := strings.Join([]string{
		"> **Estado:** `open`",
		"> **Espera:** victor",
		"> **Qué espera:** que apruebe el **texto** del `aviso`",
		"",
		"## Descripción",
		"",
		"> **Qué espera:** otra cosa",
	}, "\n")
	cases := map[string]struct{ in, want string }{
		"con > y marcas":         {quoted, "que apruebe el texto del aviso"},
		"sin > y sin acento":     {"**Estado:** open\r\n**Que espera:** el visto bueno\r\n", "el visto bueno"},
		"mayúsculas":             {"> **QUÉ ESPERA:** la firma", "la firma"},
		"solo después del ##":    {"Intro\n## Detalle\n**Qué espera:** tarde", ""},
		"sin la línea":           {"> **Espera:** victor\n\nTexto.", ""},
		"vacía":                  {"", ""},
		"rótulo sin texto":       {"**Qué espera:**", ""},
		"### no corta, ## sí":    {"### Sub\n**Qué espera:** algo", "algo"},
		"##sin espacio no corta": {"##x\n**Qué espera:** algo", "algo"},
	}
	for name, c := range cases {
		if got := model.WaitingNote(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestShareLinkExpiry(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for d, days := range map[model.ShareDuration]int{
		model.ShareDurationSevenDays:  7,
		model.ShareDurationThirtyDays: 30,
		model.ShareDurationNinetyDays: 90,
	} {
		got, err := model.ShareLinkExpiry(now, d)
		if err != nil || got == nil || !got.Equal(now.AddDate(0, 0, days)) {
			t.Errorf("%d días: got %v, %v", days, got, err)
		}
	}
	if got, err := model.ShareLinkExpiry(now, model.ShareDurationNever); got != nil || err != nil {
		t.Errorf("sin vencimiento: got %v, %v", got, err)
	}
	for _, d := range []model.ShareDuration{model.ShareDurationUnspecified, 99} {
		if _, err := model.ShareLinkExpiry(now, d); !domainerrors.IsValidation(err) {
			t.Errorf("duración %d: got %v, want validación", d, err)
		}
	}
}

func TestLooksLikeShareToken(t *testing.T) {
	ok := "tks_" + strings.Repeat("A", 43)
	if !model.LooksLikeShareToken(ok) {
		t.Errorf("%q debería parecer un token de enlace", ok)
	}
	for _, s := range []string{"", "tks_", ok + "A", ok[:len(ok)-1], "tka_" + strings.Repeat("A", 43), strings.Repeat("A", 47)} {
		if model.LooksLikeShareToken(s) {
			t.Errorf("%q no debería parecer un token de enlace", s)
		}
	}
}

func TestShareLink_IsActive(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	cases := map[string]struct {
		link model.ShareLink
		want bool
	}{
		"sin vencimiento": {model.ShareLink{}, true},
		"por vencer":      {model.ShareLink{ExpiresAt: &future}, true},
		"vencido":         {model.ShareLink{ExpiresAt: &past}, false},
		"vence justo":     {model.ShareLink{ExpiresAt: &now}, false},
		"revocado":        {model.ShareLink{RevokedAt: &past}, false},
	}
	for name, c := range cases {
		if got := c.link.IsActive(now); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}

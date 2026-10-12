package model

import (
	"strconv"
	"strings"
	"unicode/utf8"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// IssueSearchMaxLen es el largo máximo del texto de búsqueda de ListIssues.
const IssueSearchMaxLen = 200

// IssueOrder es el orden de ListIssues. Los empates se resuelven por rank,
// así el orden es estable entre páginas.
type IssueOrder string

// Órdenes válidos. El vacío es "sin especificar": rank.
const (
	IssueOrderUnspecified  IssueOrder = ""
	IssueOrderRank         IssueOrder = "rank"
	IssueOrderUpdatedDesc  IssueOrder = "updated_desc"
	IssueOrderPriorityDesc IssueOrder = "priority_desc"
)

// IsValid informa si el orden es uno de los definidos (el vacío no lo es).
func (o IssueOrder) IsValid() bool {
	switch o {
	case IssueOrderRank, IssueOrderUpdatedDesc, IssueOrderPriorityDesc:
		return true
	}
	return false
}

// IssueSearch es el texto de búsqueda de ListIssues ya interpretado. Text
// vacío = sin búsqueda.
type IssueSearch struct {
	// Text coincide con parte del título, sin distinguir mayúsculas ni
	// acentos.
	Text string
	// Number > 0: el texto es un número ("12") o una clave del proyecto
	// ("TK-12"), y también trae ese issue.
	Number int32
}

// ParseIssueSearch interpreta el query de ListIssues para el proyecto de
// clave projectKey. Vacío o solo espacios: sin búsqueda; más de
// IssueSearchMaxLen runas (sin los espacios de los extremos), ValidationError.
func ParseIssueSearch(raw, projectKey string) (IssueSearch, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return IssueSearch{}, nil
	}
	if utf8.RuneCountInString(text) > IssueSearchMaxLen {
		return IssueSearch{}, domainerrors.ValidationError("query is too long", map[string]any{"field": "query", "max_len": IssueSearchMaxLen})
	}
	digits := text
	if key, num, ok := strings.Cut(text, "-"); ok && projectKey != "" && strings.EqualFold(key, projectKey) {
		digits = num
	}
	return IssueSearch{Text: text, Number: parseIssueNumber(digits)}, nil
}

// parseIssueNumber devuelve el número si s son solo dígitos y cabe en un
// número de issue; si no, 0.
func parseIssueNumber(s string) int32 {
	if s == "" {
		return 0
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
	}
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil || n <= 0 {
		return 0
	}
	return int32(n)
}

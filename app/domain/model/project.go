package model

import (
	"strings"
	"time"
	"unicode/utf8"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// Límites de Project.
const (
	ProjectKeyMinLen         = 2
	ProjectKeyMaxLen         = 10
	ProjectNameMaxLen        = 100
	ProjectDescriptionMaxLen = 2000
)

// Project agrupa issues. Pertenece a un único dueño (OwnerUID, el uid que
// propaga el gateway); en la rebanada 1 no hay organizaciones ni miembros.
//
// Regla: el dominio es Go puro. Sin tags de GORM, sin tipos de proto.
type Project struct {
	ID       string
	OwnerUID string
	// Key es la clave corta del proyecto ("TK"); los issues se nombran TK-42.
	// Única por dueño.
	Key         string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NormalizeProjectKey recorta espacios, pasa a mayúsculas y valida el formato
// de la key: 2 a 10 letras A-Z o dígitos, empezando con letra.
//
// Pasar a mayúsculas es una comodidad para el cliente (CLI, formularios): "tk"
// y "TK" son la misma key, y lo que se guarda y devuelve es siempre "TK".
func NormalizeProjectKey(raw string) (string, error) {
	key := strings.ToUpper(strings.TrimSpace(raw))
	if key == "" {
		return "", domainerrors.ValidationError("key is required", map[string]any{"field": "key"})
	}
	if !isValidProjectKey(key) {
		return "", domainerrors.ValidationError("key must be 2 to 10 uppercase letters or digits, starting with a letter", map[string]any{
			"field": "key",
			"value": raw,
		})
	}
	return key, nil
}

func isValidProjectKey(key string) bool {
	if len(key) < ProjectKeyMinLen || len(key) > ProjectKeyMaxLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		isLetter := c >= 'A' && c <= 'Z'
		isDigit := c >= '0' && c <= '9'
		if i == 0 && !isLetter {
			return false
		}
		if !isLetter && !isDigit {
			return false
		}
	}
	return true
}

// NormalizeProjectName recorta espacios y valida que el nombre no sea vacío ni
// exceda ProjectNameMaxLen runas.
func NormalizeProjectName(raw string) (string, error) {
	return requiredText("name", raw, ProjectNameMaxLen)
}

// NormalizeProjectDescription recorta espacios y valida el largo (opcional).
func NormalizeProjectDescription(raw string) (string, error) {
	return optionalText("description", raw, ProjectDescriptionMaxLen)
}

// requiredText recorta y exige un texto no vacío de hasta maxLen runas.
func requiredText(field, raw string, maxLen int) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", domainerrors.ValidationError(field+" is required", map[string]any{"field": field})
	}
	return optionalText(field, s, maxLen)
}

// optionalText recorta y valida que el texto no exceda maxLen runas.
func optionalText(field, raw string, maxLen int) (string, error) {
	s := strings.TrimSpace(raw)
	if utf8.RuneCountInString(s) > maxLen {
		return "", domainerrors.ValidationError(field+" is too long", map[string]any{
			"field":   field,
			"max_len": maxLen,
		})
	}
	return s, nil
}

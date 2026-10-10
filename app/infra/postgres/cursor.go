package postgres

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Cursores keyset opacos: JSON compacto en base64 URL-safe. Cada listado
// tiene su propio struct con las columnas de su ORDER BY (más el id como
// desempate estable, salvo que la columna ya sea única). JSON en vez de un formato con separadores porque los
// nombres de proyecto pueden contener cualquier carácter.
//
// Un cursor mal formado es un error del cliente: el repo lo traduce a
// ValidationError.

// projectCursor: orden (name ASC, id ASC).
type projectCursor struct {
	Name string `json:"n"`
	ID   string `json:"i"`
}

// issueCursor: orden (rank ASC). rank es único por proyecto, así que no hace
// falta id como desempate. Los cursores del orden anterior (prioridad, con
// campos "p", "t" e "i") no decodifican: DisallowUnknownFields.
type issueCursor struct {
	Rank string `json:"r"`
}

// encodeCursor codifica cualquier struct de cursor en un string opaco.
func encodeCursor(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Solo structs propios con strings e ints: no puede fallar.
		panic(fmt.Sprintf("encodeCursor: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor decodifica s en v (puntero a un struct de cursor) rechazando
// campos desconocidos.
func decodeCursor(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("invalid cursor encoding: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid cursor format: %w", err)
	}
	return nil
}

func decodeProjectCursor(s string) (projectCursor, error) {
	var c projectCursor
	if err := decodeCursor(s, &c); err != nil {
		return projectCursor{}, err
	}
	if _, err := uuid.Parse(c.ID); err != nil {
		return projectCursor{}, errors.New("invalid cursor id")
	}
	return c, nil
}

func decodeIssueCursor(s string) (issueCursor, error) {
	var c issueCursor
	if err := decodeCursor(s, &c); err != nil {
		return issueCursor{}, err
	}
	if model.ValidateRank(c.Rank) != nil {
		return issueCursor{}, errors.New("invalid cursor rank")
	}
	return c, nil
}

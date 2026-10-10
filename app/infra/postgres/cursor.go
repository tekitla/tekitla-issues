package postgres

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Cursores keyset opacos: JSON compacto en base64 URL-safe. Cada listado
// tiene su propio struct con las columnas de su ORDER BY (más el id como
// desempate estable). JSON en vez de un formato con separadores porque los
// nombres de proyecto pueden contener cualquier carácter.
//
// Un cursor mal formado es un error del cliente: el repo lo traduce a
// ValidationError.

// projectCursor: orden (name ASC, id ASC).
type projectCursor struct {
	Name string `json:"n"`
	ID   string `json:"i"`
}

// issueCursor: orden (priority DESC, created_at DESC, id DESC).
// CreatedAt va en microsegundos (la precisión de timestamptz).
type issueCursor struct {
	Priority  int    `json:"p"`
	CreatedAt int64  `json:"t"`
	ID        string `json:"i"`
}

func (c issueCursor) createdAt() time.Time { return time.UnixMicro(c.CreatedAt).UTC() }

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
	if _, err := uuid.Parse(c.ID); err != nil {
		return issueCursor{}, errors.New("invalid cursor id")
	}
	if c.CreatedAt <= 0 {
		return issueCursor{}, errors.New("invalid cursor timestamp")
	}
	return c, nil
}

package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
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

// issueCursor: orden de ListIssues (rank ASC, o updated_at DESC / priority
// DESC con rank ASC de desempate). rank es único por proyecto, así que no
// hace falta id. Filter es la huella de los filtros y el orden
// (issueFilterFingerprint): un cursor de otra consulta no sirve. Los de v0.1
// (campos "t" e "i") no decodifican (DisallowUnknownFields) y los de v0.3
// (solo "r") no tienen huella: los dos son INVALID_ARGUMENT.
type issueCursor struct {
	Rank          string `json:"r"`
	UpdatedMicros int64  `json:"u,omitempty"`
	Priority      int    `json:"p,omitempty"`
	Filter        string `json:"f"`
}

// issueFilterFingerprint resume los filtros y el orden de ListIssues (sin
// cursor ni límite), sin depender del orden de las listas.
func issueFilterFingerprint(f port.IssueFilter) string {
	states := make([]string, len(f.States))
	for i, s := range f.States {
		states[i] = string(s)
	}
	priorities := make([]int, len(f.Priorities))
	for i, p := range f.Priorities {
		priorities[i] = int(p)
	}
	statusIDs := append([]string(nil), f.StatusIDs...)
	labelIDs := append([]string(nil), f.LabelIDs...)
	sort.Strings(states)
	sort.Ints(priorities)
	sort.Strings(statusIDs)
	sort.Strings(labelIDs)
	b, err := json.Marshal([]any{f.ProjectID, states, statusIDs, priorities, labelIDs, f.Search.Text, f.Search.Number, string(f.Order)})
	if err != nil {
		// Solo strings e ints: no puede fallar.
		panic(fmt.Sprintf("issueFilterFingerprint: %v", err))
	}
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:9])
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
	if model.ValidateRank(c.Rank) != nil || c.Filter == "" {
		return issueCursor{}, errors.New("invalid cursor rank")
	}
	return c, nil
}

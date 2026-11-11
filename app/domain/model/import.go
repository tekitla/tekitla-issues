package model

import (
	"fmt"
	"strings"
	"time"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// SourceKeyMaxLen es el largo máximo de la clave de origen de un issue
// importado.
const SourceKeyMaxLen = 200

// ImportOutcome es lo que hizo ImportIssue.
type ImportOutcome string

// Resultados de ImportIssue.
const (
	ImportCreated   ImportOutcome = "created"
	ImportUpdated   ImportOutcome = "updated"
	ImportUnchanged ImportOutcome = "unchanged"
)

// ImportSpec es un issue importado de otro tracker, ya normalizado y
// validado (issue 025). El proyecto es un espejo del origen: si el issue ya
// existe (misma SourceKey), se pisan título, descripción, prioridad,
// categoría y las etiquetas que puso el importador; las puestas a mano no se
// tocan.
type ImportSpec struct {
	ProjectID string
	SourceKey string
	Number    int32
	// CreatedAt: al crear, o si el issue ya existe y OverrideCreatedAt
	// (issue 036): corrige created_at y la fecha de su evento «creado».
	CreatedAt         time.Time
	OverrideCreatedAt bool
	Title             string
	Description       string
	Priority          Priority
	State             IssueState
	// LabelNames son las etiquetas del importador, sin repetidos (sin
	// distinguir mayúsculas). Las que no existen se crean con color neutral.
	LabelNames []string
}

// NormalizeSourceKey recorta espacios y valida la clave de origen (1 a
// SourceKeyMaxLen runas).
func NormalizeSourceKey(raw string) (string, error) {
	return requiredText("source_key", raw, SourceKeyMaxLen)
}

// NormalizeImportLabels normaliza los nombres de etiqueta y quita los
// repetidos sin distinguir mayúsculas (se queda el primero). Valida cada
// nombre y el máximo por issue.
func NormalizeImportLabels(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, raw := range names {
		name, err := NormalizeLabelName(raw)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	if len(out) > MaxLabelsPerIssue {
		return nil, TooManyLabelsError()
	}
	return out, nil
}

// TooManyLabelsError es el error de pasarse de MaxLabelsPerIssue con las
// etiquetas de un import.
func TooManyLabelsError() error {
	return domainerrors.ValidationError("too many labels", map[string]any{"field": "label_names", "max": MaxLabelsPerIssue})
}

// Límites de ImportIssueEvents (issue 036).
const (
	ImportEventsMax = 500
	SourceRefMaxLen = 200
)

// ImportedEvent es un evento con su fecha en el origen, ya normalizado y
// validado (ver NormalizeImportedEvents). Hoy solo cambios de estado.
type ImportedEvent struct {
	// SourceRef reconoce el evento en el issue (p. ej. el sha del commit).
	SourceRef  string
	Source     EventSource
	OccurredAt time.Time
	// Status: categorías y valores del origen; los ids van vacíos.
	Status *StatusChange
}

// ImportEventsResult es lo que hizo ImportIssueEvents.
type ImportEventsResult struct {
	Created int
	Skipped int
	Updated int
}

// NormalizeImportedEvents valida los eventos de un ImportIssueEvents: 1 a
// ImportEventsMax, source_ref de 1 a SourceRefMaxLen runas y sin repetir,
// fuente IMPORT_HISTORY o IMPORT_FILE, fecha presente y no en el futuro
// (now, con un día de margen por las fechas sin hora), cambio de estado con
// categorías válidas y nombres de 1 a StatusNameMaxLen. Que no sea anterior
// al alta del issue lo verifica quien lo guarda.
func NormalizeImportedEvents(events []ImportedEvent, now time.Time) ([]ImportedEvent, error) {
	if len(events) == 0 || len(events) > ImportEventsMax {
		return nil, domainerrors.ValidationError("events must have 1 to 500 items", map[string]any{"field": "events", "max": ImportEventsMax})
	}
	out := make([]ImportedEvent, len(events))
	seen := make(map[string]bool, len(events))
	for i, ev := range events {
		field := func(name string) string { return fmt.Sprintf("events[%d].%s", i, name) }
		ref, err := requiredText(field("source_ref"), ev.SourceRef, SourceRefMaxLen)
		if err != nil {
			return nil, err
		}
		if seen[ref] {
			return nil, domainerrors.ValidationError("repeated source_ref", map[string]any{"field": field("source_ref"), "source_ref": ref})
		}
		seen[ref] = true
		if ev.Source != EventSourceImportHistory && ev.Source != EventSourceImportFile {
			return nil, domainerrors.ValidationError("source must be IMPORT_HISTORY or IMPORT_FILE", map[string]any{"field": field("source")})
		}
		if ev.OccurredAt.IsZero() {
			return nil, domainerrors.ValidationError("occurred_at is required", map[string]any{"field": field("occurred_at")})
		}
		if ev.OccurredAt.After(now.Add(24 * time.Hour)) {
			return nil, domainerrors.ValidationError("occurred_at must not be in the future", map[string]any{"field": field("occurred_at")})
		}
		if ev.Status == nil {
			return nil, domainerrors.ValidationError("change is required", map[string]any{"field": field("change")})
		}
		if !ev.Status.FromCategory.IsValid() || !ev.Status.ToCategory.IsValid() {
			return nil, domainerrors.ValidationError("invalid state", map[string]any{"field": field("status_changed")})
		}
		from, err := requiredText(field("status_changed.from_name"), ev.Status.FromName, StatusNameMaxLen)
		if err != nil {
			return nil, err
		}
		to, err := requiredText(field("status_changed.to_name"), ev.Status.ToName, StatusNameMaxLen)
		if err != nil {
			return nil, err
		}
		out[i] = ImportedEvent{
			SourceRef: ref, Source: ev.Source, OccurredAt: ev.OccurredAt,
			Status: &StatusChange{FromName: from, FromCategory: ev.Status.FromCategory, ToName: to, ToCategory: ev.Status.ToCategory},
		}
	}
	return out, nil
}

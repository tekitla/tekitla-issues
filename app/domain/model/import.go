package model

import (
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
	// CreatedAt: solo al crear.
	CreatedAt   time.Time
	Title       string
	Description string
	Priority    Priority
	State       IssueState
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
		return nil, tooManyLabels()
	}
	return out, nil
}

func tooManyLabels() error {
	return domainerrors.ValidationError("too many labels", map[string]any{"field": "label_names", "max": MaxLabelsPerIssue})
}

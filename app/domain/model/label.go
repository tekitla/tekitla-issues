package model

import "time"

// Límites de Label.
const (
	LabelNameMaxLen     = 40
	MaxLabelsPerProject = 100
	MaxLabelsPerIssue   = 20
)

// LabelColor es un tono del design system, no un color libre. El valor
// numérico es el que se guarda en la base (mismo orden que el enum del proto).
type LabelColor int

// Colores válidos. 0 es "sin especificar": se guarda como Neutral.
const (
	LabelColorUnspecified LabelColor = 0
	LabelColorNeutral     LabelColor = 1
	LabelColorInfo        LabelColor = 2
	LabelColorSuccess     LabelColor = 3
	LabelColorWarning     LabelColor = 4
	LabelColorDanger      LabelColor = 5
	LabelColorAccent      LabelColor = 6
	LabelColorBrand       LabelColor = 7
)

// IsValid informa si el color es uno de los definidos (sin especificar no lo
// es).
func (c LabelColor) IsValid() bool {
	return c >= LabelColorNeutral && c <= LabelColorBrand
}

// Label es una etiqueta de un proyecto.
type Label struct {
	ID        string
	ProjectID string
	// Name es único en el proyecto sin distinguir mayúsculas.
	Name      string
	Color     LabelColor
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NormalizeLabelName recorta espacios y valida que el nombre no sea vacío ni
// exceda LabelNameMaxLen runas.
func NormalizeLabelName(raw string) (string, error) {
	return requiredText("name", raw, LabelNameMaxLen)
}

package model

import (
	"time"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// Límites de Status.
const (
	StatusNameMaxLen      = 40
	MaxStatusesPerProject = 20
)

// Status es un estado concreto de un proyecto: una columna del tablero. Cada
// uno pertenece a una categoría fija (IssueState) que no cambia; Issue.State
// es siempre la categoría de su Status.
type Status struct {
	ID        string
	ProjectID string
	// Name es único en el proyecto sin distinguir mayúsculas.
	Name     string
	Category IssueState
	// Rank es la posición en el orden de columnas (ver rank.go).
	Rank string
	// Color es el tono de la columna; siempre válido (sin especificar se
	// guarda como DefaultStatusColor de la categoría).
	Color LabelColor
	// WIPLimit es el máximo de issues de la columna; 0 = sin límite. Pasarse
	// no bloquea: el panel marca la columna.
	WIPLimit int32
	// Hidden: la columna no se muestra en el tablero.
	Hidden    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DefaultStatus es un estado con el que nace todo proyecto.
type DefaultStatus struct {
	Name     string
	Category IssueState
}

// DefaultStatuses son los estados de un proyecto nuevo, en orden: uno por
// categoría. La migración 000003 hace el mismo backfill para los proyectos
// existentes.
var DefaultStatuses = []DefaultStatus{
	{Name: "Por hacer", Category: IssueStateTodo},
	{Name: "En curso", Category: IssueStateInProgress},
	{Name: "Hecho", Category: IssueStateDone},
	{Name: "Descartado", Category: IssueStateCanceled},
}

// DefaultStatusColor es el tono de cada categoría: el que recibe un estado
// creado o actualizado con color sin especificar (y el del backfill de la
// migración 000004).
func DefaultStatusColor(category IssueState) LabelColor {
	switch category {
	case IssueStateInProgress:
		return LabelColorInfo
	case IssueStateDone:
		return LabelColorSuccess
	case IssueStateCanceled:
		return LabelColorDanger
	}
	return LabelColorNeutral
}

// DefaultStatusHidden informa si un estado de la categoría nace oculto: los
// CANCELED (descartados) no se muestran en el tablero.
func DefaultStatusHidden(category IssueState) bool {
	return category == IssueStateCanceled
}

// ResolveStatusColor valida el color pedido para un estado de la categoría:
// sin especificar da DefaultStatusColor; uno fuera de rango, ValidationError.
func ResolveStatusColor(color LabelColor, category IssueState) (LabelColor, error) {
	if color == LabelColorUnspecified {
		return DefaultStatusColor(category), nil
	}
	if !color.IsValid() {
		return 0, domainerrors.ValidationError("invalid color", map[string]any{"field": "color", "value": int(color)})
	}
	return color, nil
}

// ValidateWIPLimit valida el límite WIP (0 = sin límite; negativo no).
func ValidateWIPLimit(limit int32) error {
	if limit < 0 {
		return domainerrors.ValidationError("wip_limit must not be negative", map[string]any{"field": "wip_limit", "value": limit})
	}
	return nil
}

// NormalizeStatusName recorta espacios y valida que el nombre no sea vacío ni
// exceda StatusNameMaxLen runas.
func NormalizeStatusName(raw string) (string, error) {
	return requiredText("name", raw, StatusNameMaxLen)
}

// FirstStatusOf devuelve el primer estado (por rank) de la categoría, o nil.
// statuses tiene que venir ordenado por rank.
func FirstStatusOf(statuses []*Status, category IssueState) *Status {
	for _, s := range statuses {
		if s.Category == category {
			return s
		}
	}
	return nil
}

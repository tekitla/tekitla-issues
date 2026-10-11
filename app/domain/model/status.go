package model

import "time"

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
	Rank      string
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

package model

import "time"

// Límites de Issue.
const (
	IssueTitleMaxLen       = 200
	IssueDescriptionMaxLen = 20000
)

// IssueState es la categoría fija del estado de un issue. En la rebanada 1 hay
// un estado por categoría; los estados configurables por proyecto (rebanada 3)
// se cuelgan de una de estas categorías.
//
// El valor vacío significa "sin especificar": como filtro de ListIssues es
// "todos los estados"; como destino de MoveIssue es inválido.
type IssueState string

// Estados válidos. El string es el valor que se guarda en issues.issues.state.
const (
	IssueStateUnspecified IssueState = ""
	IssueStateTodo        IssueState = "todo"
	IssueStateInProgress  IssueState = "in_progress"
	IssueStateDone        IssueState = "done"
	// IssueStateCanceled es "descartado": cerrado sin hacerse.
	IssueStateCanceled IssueState = "canceled"
)

// IsValid informa si el estado es una de las categorías fijas (el vacío no
// lo es).
func (s IssueState) IsValid() bool {
	switch s {
	case IssueStateTodo, IssueStateInProgress, IssueStateDone, IssueStateCanceled:
		return true
	}
	return false
}

// Priority es la prioridad de un issue. El valor numérico define el orden
// (mayor = más urgente) y es lo que se guarda en la base.
type Priority int

// Prioridades válidas. 0 es "sin especificar": al crear se guarda como Medium.
const (
	PriorityUnspecified Priority = 0
	PriorityLow         Priority = 1
	PriorityMedium      Priority = 2
	PriorityHigh        Priority = 3
	PriorityUrgent      Priority = 4
)

// DefaultPriority es la que recibe un issue creado sin prioridad.
const DefaultPriority = PriorityMedium

// IsValid informa si la prioridad es una de las cuatro definidas.
func (p Priority) IsValid() bool {
	return p >= PriorityLow && p <= PriorityUrgent
}

// Issue es una tarea dentro de un Project. Number es correlativo por proyecto
// desde 1 y no se reutiliza (lo asigna el repo al crear).
type Issue struct {
	ID        string
	ProjectID string
	Number    int32
	Title     string
	// Description es Markdown.
	Description string
	State       IssueState
	Priority    Priority
	// Rank es la posición en el orden manual del proyecto (ver rank.go). La
	// asigna el repo al crear: al final del orden.
	Rank      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NormalizeIssueTitle recorta espacios y valida que el título no sea vacío ni
// exceda IssueTitleMaxLen runas.
func NormalizeIssueTitle(raw string) (string, error) {
	return requiredText("title", raw, IssueTitleMaxLen)
}

// NormalizeIssueDescription recorta espacios y valida el largo (opcional).
func NormalizeIssueDescription(raw string) (string, error) {
	return optionalText("description", raw, IssueDescriptionMaxLen)
}

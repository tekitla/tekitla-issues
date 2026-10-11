package model

import "time"

// Límites de Comment.
const CommentBodyMaxLen = 10000

// Comment es un comentario en un issue (Markdown).
type Comment struct {
	ID      string
	IssueID string
	// AuthorUID es el uid de quien lo escribió.
	AuthorUID string
	Body      string
	CreatedAt time.Time
}

// NormalizeCommentBody recorta espacios y valida que el cuerpo no sea vacío
// ni exceda CommentBodyMaxLen runas.
func NormalizeCommentBody(raw string) (string, error) {
	return requiredText("body", raw, CommentBodyMaxLen)
}

// EventKind es el tipo de cambio de un IssueEvent. El string es el que se
// guarda en issues.issue_events.kind.
type EventKind string

// Tipos de evento.
const (
	EventCreated            EventKind = "created"
	EventStatusChanged      EventKind = "status_changed"
	EventTitleChanged       EventKind = "title_changed"
	EventDescriptionChanged EventKind = "description_changed"
	EventPriorityChanged    EventKind = "priority_changed"
	EventLabelsChanged      EventKind = "labels_changed"
	EventCommented          EventKind = "commented"
)

// IssueEvent es un cambio en un issue, para su historial. Según Kind, viene
// lleno uno de Status, Title, Priority, Labels o Comment (Created y
// DescriptionChanged no tienen detalle).
type IssueEvent struct {
	ID        string
	IssueID   string
	ActorUID  string
	Kind      EventKind
	Status    *StatusChange
	Title     *TitleChange
	Priority  *PriorityChange
	Labels    *LabelsChange
	Comment   *Comment
	CreatedAt time.Time
}

// StatusChange guarda id, nombre y categoría de los dos estados al momento
// del cambio: el historial se sigue leyendo aunque después se renombren o se
// borren.
type StatusChange struct {
	FromID       string
	FromName     string
	FromCategory IssueState
	ToID         string
	ToName       string
	ToCategory   IssueState
}

// NewStatusChange arma el cambio entre dos estados.
func NewStatusChange(from, to *Status) *StatusChange {
	return &StatusChange{
		FromID: from.ID, FromName: from.Name, FromCategory: from.Category,
		ToID: to.ID, ToName: to.Name, ToCategory: to.Category,
	}
}

// TitleChange es el título antes y después.
type TitleChange struct {
	From string
	To   string
}

// PriorityChange es la prioridad antes y después.
type PriorityChange struct {
	From Priority
	To   Priority
}

// LabelsChange son las etiquetas agregadas y quitadas, con sus nombres al
// momento del cambio.
type LabelsChange struct {
	Added   []*Label
	Removed []*Label
}

// IssuePatch son los cambios que pide UpdateIssue: nil = no cambia. Los
// valores ya vienen normalizados y validados.
type IssuePatch struct {
	Title       *string
	Description *string
	Priority    *Priority
	// LabelIDs reemplaza las etiquetas (nil = no cambian; vacío = sin
	// etiquetas). Sin repetidos.
	LabelIDs *[]string
}

// IsEmpty informa si el patch no pide ningún cambio.
func (p IssuePatch) IsEmpty() bool {
	return p.Title == nil && p.Description == nil && p.Priority == nil && p.LabelIDs == nil
}

// IssueChanges compara el issue con el patch y devuelve los eventos de lo que
// de verdad cambia (sin ID, actor ni fecha: los pone quien los guarda).
// labels son las etiquetas del proyecto por id, para guardar sus nombres.
func IssueChanges(issue *Issue, patch IssuePatch, labels map[string]*Label) []*IssueEvent {
	var events []*IssueEvent
	if patch.Title != nil && *patch.Title != issue.Title {
		events = append(events, &IssueEvent{Kind: EventTitleChanged, Title: &TitleChange{From: issue.Title, To: *patch.Title}})
	}
	if patch.Description != nil && *patch.Description != issue.Description {
		events = append(events, &IssueEvent{Kind: EventDescriptionChanged})
	}
	if patch.Priority != nil && *patch.Priority != issue.Priority {
		events = append(events, &IssueEvent{Kind: EventPriorityChanged, Priority: &PriorityChange{From: issue.Priority, To: *patch.Priority}})
	}
	if patch.LabelIDs != nil {
		added, removed := diffIDs(issue.LabelIDs, *patch.LabelIDs)
		if len(added) > 0 || len(removed) > 0 {
			change := &LabelsChange{}
			for _, id := range added {
				change.Added = append(change.Added, labelOrID(labels, id))
			}
			for _, id := range removed {
				change.Removed = append(change.Removed, labelOrID(labels, id))
			}
			events = append(events, &IssueEvent{Kind: EventLabelsChanged, Labels: change})
		}
	}
	return events
}

// diffIDs devuelve los ids de next que no están en prev y los de prev que no
// están en next, en el orden de cada lista.
func diffIDs(prev, next []string) (added, removed []string) {
	inPrev := make(map[string]bool, len(prev))
	for _, id := range prev {
		inPrev[id] = true
	}
	inNext := make(map[string]bool, len(next))
	for _, id := range next {
		inNext[id] = true
		if !inPrev[id] {
			added = append(added, id)
		}
	}
	for _, id := range prev {
		if !inNext[id] {
			removed = append(removed, id)
		}
	}
	return added, removed
}

func labelOrID(labels map[string]*Label, id string) *Label {
	if l, ok := labels[id]; ok {
		return l
	}
	return &Label{ID: id}
}

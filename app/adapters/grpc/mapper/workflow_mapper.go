package mapper

import (
	"fmt"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Mappers de la rebanada 3a: estados, etiquetas, comentarios e historial.

// StatusToProto convierte un Status del dominio a su mensaje proto.
func StatusToProto(m *model.Status) *issuesv1.Status {
	if m == nil {
		return nil
	}
	return &issuesv1.Status{
		Id:        m.ID,
		ProjectId: m.ProjectID,
		Name:      m.Name,
		Category:  IssueStateToProto(m.Category),
		Rank:      m.Rank,
		Color:     LabelColorToProto(m.Color),
		WipLimit:  m.WIPLimit,
		Hidden:    m.Hidden,
	}
}

// StatusSpecsFromProto convierte la lista de BatchUpdateStatuses.
func StatusSpecsFromProto(specs []*issuesv1.StatusSpec) []model.StatusSpec {
	out := make([]model.StatusSpec, len(specs))
	for i, s := range specs {
		out[i] = model.StatusSpec{
			ID:       s.GetId(),
			Name:     s.GetName(),
			Category: IssueStateFromProto(s.GetCategory()),
			Color:    LabelColorFromProto(s.GetColor()),
			WIPLimit: s.GetWipLimit(),
			Hidden:   s.GetHidden(),
		}
	}
	return out
}

// StatusDeletionsFromProto convierte los borrados de BatchUpdateStatuses.
func StatusDeletionsFromProto(deletions []*issuesv1.StatusDeletion) []model.StatusDeletion {
	out := make([]model.StatusDeletion, len(deletions))
	for i, d := range deletions {
		out[i] = model.StatusDeletion{StatusID: d.GetStatusId(), MoveToStatusID: d.GetMoveToStatusId()}
	}
	return out
}

// StatusesToProto convierte un slice de Statuses.
func StatusesToProto(statuses []*model.Status) []*issuesv1.Status {
	out := make([]*issuesv1.Status, len(statuses))
	for i, s := range statuses {
		out[i] = StatusToProto(s)
	}
	return out
}

// LabelColorFromProto convierte el enum proto al color del dominio.
// UNSPECIFIED -> LabelColorUnspecified; un valor desconocido queda fuera de
// rango y la capa de aplicación lo rechaza.
func LabelColorFromProto(c issuesv1.LabelColor) model.LabelColor {
	if c == issuesv1.LabelColor_LABEL_COLOR_UNSPECIFIED {
		return model.LabelColorUnspecified
	}
	color := model.LabelColor(c)
	if !color.IsValid() {
		return model.LabelColor(-1)
	}
	return color
}

// LabelColorToProto convierte el color del dominio al enum proto (mismos
// números).
func LabelColorToProto(c model.LabelColor) issuesv1.LabelColor {
	if !c.IsValid() {
		return issuesv1.LabelColor_LABEL_COLOR_UNSPECIFIED
	}
	return issuesv1.LabelColor(c)
}

// LabelToProto convierte un Label del dominio a su mensaje proto.
func LabelToProto(m *model.Label) *issuesv1.Label {
	if m == nil {
		return nil
	}
	return &issuesv1.Label{
		Id:        m.ID,
		ProjectId: m.ProjectID,
		Name:      m.Name,
		Color:     LabelColorToProto(m.Color),
	}
}

// LabelsToProto convierte un slice de Labels.
func LabelsToProto(labels []*model.Label) []*issuesv1.Label {
	out := make([]*issuesv1.Label, len(labels))
	for i, l := range labels {
		out[i] = LabelToProto(l)
	}
	return out
}

// CommentToProto convierte un Comment del dominio a su mensaje proto.
func CommentToProto(m *model.Comment) *issuesv1.Comment {
	if m == nil {
		return nil
	}
	return &issuesv1.Comment{
		Id:        m.ID,
		IssueId:   m.IssueID,
		AuthorId:  m.AuthorUID,
		Body:      m.Body,
		CreatedAt: TimestampToProto(m.CreatedAt),
	}
}

// IssueEventToProto convierte un evento del historial. Un Kind desconocido
// es un error de programación (todos los tipos del dominio tienen su caso).
func IssueEventToProto(m *model.IssueEvent) (*issuesv1.IssueEvent, error) {
	out := &issuesv1.IssueEvent{
		Id:        m.ID,
		IssueId:   m.IssueID,
		ActorId:   m.ActorUID,
		CreatedAt: TimestampToProto(m.CreatedAt),
	}
	switch m.Kind {
	case model.EventCreated:
		out.Change = &issuesv1.IssueEvent_Created{Created: &issuesv1.IssueCreated{}}
	case model.EventStatusChanged:
		c := m.Status
		out.Change = &issuesv1.IssueEvent_StatusChanged{StatusChanged: &issuesv1.StatusChanged{
			FromStatusId: c.FromID, FromStatusName: c.FromName, FromState: IssueStateToProto(c.FromCategory),
			ToStatusId: c.ToID, ToStatusName: c.ToName, ToState: IssueStateToProto(c.ToCategory),
		}}
	case model.EventTitleChanged:
		out.Change = &issuesv1.IssueEvent_TitleChanged{TitleChanged: &issuesv1.TitleChanged{From: m.Title.From, To: m.Title.To}}
	case model.EventDescriptionChanged:
		out.Change = &issuesv1.IssueEvent_DescriptionChanged{DescriptionChanged: &issuesv1.DescriptionChanged{}}
	case model.EventPriorityChanged:
		out.Change = &issuesv1.IssueEvent_PriorityChanged{PriorityChanged: &issuesv1.PriorityChanged{
			From: PriorityToProto(m.Priority.From), To: PriorityToProto(m.Priority.To),
		}}
	case model.EventLabelsChanged:
		change := &issuesv1.LabelsChanged{}
		for _, l := range m.Labels.Added {
			change.AddedLabelIds = append(change.AddedLabelIds, l.ID)
			change.AddedLabelNames = append(change.AddedLabelNames, l.Name)
		}
		for _, l := range m.Labels.Removed {
			change.RemovedLabelIds = append(change.RemovedLabelIds, l.ID)
			change.RemovedLabelNames = append(change.RemovedLabelNames, l.Name)
		}
		out.Change = &issuesv1.IssueEvent_LabelsChanged{LabelsChanged: change}
	case model.EventCommented:
		out.Change = &issuesv1.IssueEvent_Commented{Commented: CommentToProto(m.Comment)}
	case model.EventDiscarded:
		c := m.Status
		out.Change = &issuesv1.IssueEvent_Discarded{Discarded: &issuesv1.IssueDiscarded{
			Reason:       m.Reason,
			FromStatusId: c.FromID, FromStatusName: c.FromName, FromState: IssueStateToProto(c.FromCategory),
			ToStatusId: c.ToID, ToStatusName: c.ToName,
		}}
	case model.EventRestored:
		c := m.Status
		out.Change = &issuesv1.IssueEvent_Restored{Restored: &issuesv1.IssueRestored{
			FromStatusId: c.FromID, FromStatusName: c.FromName,
			ToStatusId: c.ToID, ToStatusName: c.ToName,
		}}
	default:
		return nil, fmt.Errorf("unknown event kind %q", m.Kind)
	}
	return out, nil
}

// IssueEventsToProto convierte un slice de eventos.
func IssueEventsToProto(events []*model.IssueEvent) ([]*issuesv1.IssueEvent, error) {
	out := make([]*issuesv1.IssueEvent, len(events))
	for i, e := range events {
		ev, err := IssueEventToProto(e)
		if err != nil {
			return nil, err
		}
		out[i] = ev
	}
	return out, nil
}

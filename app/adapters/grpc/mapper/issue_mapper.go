// Package mapper es la ÚNICA frontera proto <-> dominio. Ni el dominio ni la
// capa de aplicación importan tipos generados de proto.
package mapper

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// ProjectToProto convierte un Project del dominio a su mensaje proto.
func ProjectToProto(m *model.Project) *issuesv1.Project {
	if m == nil {
		return nil
	}
	return &issuesv1.Project{
		Id:          m.ID,
		WorkspaceId: m.WorkspaceID,
		Key:         m.Key,
		Name:        m.Name,
		Description: m.Description,
		CreatedAt:   TimestampToProto(m.CreatedAt),
		UpdatedAt:   TimestampToProto(m.UpdatedAt),
	}
}

// ProjectsToProto convierte un slice de Projects.
func ProjectsToProto(projects []*model.Project) []*issuesv1.Project {
	out := make([]*issuesv1.Project, len(projects))
	for i, p := range projects {
		out[i] = ProjectToProto(p)
	}
	return out
}

// IssueToProto convierte un Issue del dominio a su mensaje proto.
func IssueToProto(m *model.Issue) *issuesv1.Issue {
	if m == nil {
		return nil
	}
	return &issuesv1.Issue{
		Id:          m.ID,
		ProjectId:   m.ProjectID,
		Number:      m.Number,
		Title:       m.Title,
		Description: m.Description,
		State:       IssueStateToProto(m.State),
		Priority:    PriorityToProto(m.Priority),
		Rank:        m.Rank,
		StatusId:    m.StatusID,
		LabelIds:    m.LabelIDs,
		Discard:     DiscardToProto(m.Discard),
		CreatedAt:   TimestampToProto(m.CreatedAt),
		UpdatedAt:   TimestampToProto(m.UpdatedAt),
	}
}

// DiscardToProto convierte el motivo del descarte (nil -> ausente).
func DiscardToProto(d *model.DiscardInfo) *issuesv1.DiscardInfo {
	if d == nil {
		return nil
	}
	return &issuesv1.DiscardInfo{Reason: d.Reason, ActorId: d.ActorUID, DiscardedAt: TimestampToProto(d.DiscardedAt)}
}

// IssueStatesFromProto convierte una lista de estados (filtro de ListIssues).
func IssueStatesFromProto(states []issuesv1.IssueState) []model.IssueState {
	out := make([]model.IssueState, len(states))
	for i, s := range states {
		out[i] = IssueStateFromProto(s)
	}
	return out
}

// PrioritiesFromProto convierte una lista de prioridades (filtro de
// ListIssues).
func PrioritiesFromProto(priorities []issuesv1.Priority) []model.Priority {
	out := make([]model.Priority, len(priorities))
	for i, p := range priorities {
		out[i] = PriorityFromProto(p)
	}
	return out
}

// IssueOrderFromProto convierte el orden de ListIssues. UNSPECIFIED -> vacío
// (rank); un valor desconocido queda inválido y la capa de aplicación lo
// rechaza.
func IssueOrderFromProto(o issuesv1.IssueOrder) model.IssueOrder {
	switch o {
	case issuesv1.IssueOrder_ISSUE_ORDER_UNSPECIFIED:
		return model.IssueOrderUnspecified
	case issuesv1.IssueOrder_ISSUE_ORDER_RANK:
		return model.IssueOrderRank
	case issuesv1.IssueOrder_ISSUE_ORDER_UPDATED_DESC:
		return model.IssueOrderUpdatedDesc
	case issuesv1.IssueOrder_ISSUE_ORDER_PRIORITY_DESC:
		return model.IssueOrderPriorityDesc
	}
	return model.IssueOrder(fmt.Sprintf("unknown(%d)", int32(o)))
}

// IssuesToProto convierte un slice de Issues.
func IssuesToProto(issues []*model.Issue) []*issuesv1.Issue {
	out := make([]*issuesv1.Issue, len(issues))
	for i, it := range issues {
		out[i] = IssueToProto(it)
	}
	return out
}

// IssueStateFromProto convierte el enum proto al estado del dominio.
// UNSPECIFIED -> IssueStateUnspecified (""). Un valor desconocido (cliente con
// un proto más nuevo) se convierte en un estado inválido para que la capa de
// aplicación lo rechace con INVALID_ARGUMENT en vez de ignorarlo.
func IssueStateFromProto(s issuesv1.IssueState) model.IssueState {
	switch s {
	case issuesv1.IssueState_ISSUE_STATE_UNSPECIFIED:
		return model.IssueStateUnspecified
	case issuesv1.IssueState_ISSUE_STATE_TODO:
		return model.IssueStateTodo
	case issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS:
		return model.IssueStateInProgress
	case issuesv1.IssueState_ISSUE_STATE_DONE:
		return model.IssueStateDone
	case issuesv1.IssueState_ISSUE_STATE_CANCELED:
		return model.IssueStateCanceled
	}
	return model.IssueState(fmt.Sprintf("unknown(%d)", int32(s)))
}

// IssueStateToProto convierte el estado del dominio al enum proto.
func IssueStateToProto(s model.IssueState) issuesv1.IssueState {
	switch s {
	case model.IssueStateTodo:
		return issuesv1.IssueState_ISSUE_STATE_TODO
	case model.IssueStateInProgress:
		return issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS
	case model.IssueStateDone:
		return issuesv1.IssueState_ISSUE_STATE_DONE
	case model.IssueStateCanceled:
		return issuesv1.IssueState_ISSUE_STATE_CANCELED
	}
	return issuesv1.IssueState_ISSUE_STATE_UNSPECIFIED
}

// PriorityFromProto convierte el enum proto a la prioridad del dominio.
// UNSPECIFIED -> PriorityUnspecified; un valor desconocido queda fuera de
// rango y la capa de aplicación lo rechaza.
func PriorityFromProto(p issuesv1.Priority) model.Priority {
	switch p {
	case issuesv1.Priority_PRIORITY_UNSPECIFIED:
		return model.PriorityUnspecified
	case issuesv1.Priority_PRIORITY_LOW:
		return model.PriorityLow
	case issuesv1.Priority_PRIORITY_MEDIUM:
		return model.PriorityMedium
	case issuesv1.Priority_PRIORITY_HIGH:
		return model.PriorityHigh
	case issuesv1.Priority_PRIORITY_URGENT:
		return model.PriorityUrgent
	}
	return model.Priority(-1)
}

// PriorityToProto convierte la prioridad del dominio al enum proto.
func PriorityToProto(p model.Priority) issuesv1.Priority {
	switch p {
	case model.PriorityLow:
		return issuesv1.Priority_PRIORITY_LOW
	case model.PriorityMedium:
		return issuesv1.Priority_PRIORITY_MEDIUM
	case model.PriorityHigh:
		return issuesv1.Priority_PRIORITY_HIGH
	case model.PriorityUrgent:
		return issuesv1.Priority_PRIORITY_URGENT
	}
	return issuesv1.Priority_PRIORITY_UNSPECIFIED
}

// PageFilterFromProto arma el filtro de paginación desde el PageRequest.
func PageFilterFromProto(p *issuesv1.PageRequest) port.PageFilter {
	return port.PageFilter{
		Cursor: p.GetCursor(),
		Limit:  p.GetLimit(),
	}
}

// PageResponseToProto arma el PageResponse.
func PageResponseToProto(nextCursor string, hasNext bool) *issuesv1.PageResponse {
	return &issuesv1.PageResponse{NextCursor: nextCursor, HasNext: hasNext}
}

// TimestampToProto convierte time.Time a Timestamp (zero value -> nil).
func TimestampToProto(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

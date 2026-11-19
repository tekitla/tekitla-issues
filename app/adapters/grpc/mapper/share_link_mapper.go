package mapper

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Enlaces de solo lectura (issue 009). Las vistas públicas se arman campo por
// campo en mensajes Shared*: nunca con ProjectToProto, IssueToProto ni
// ActorToProto, que llevan ids internos y uids.

// ShareScopeFromProto convierte el alcance (UNSPECIFIED o desconocido ->
// ShareScopeUnspecified, que el servicio rechaza).
func ShareScopeFromProto(s issuesv1.ShareLinkScope) model.ShareScope {
	switch s {
	case issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN:
		return model.ShareScopePlan
	case issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PANEL:
		return model.ShareScopePanel
	}
	return model.ShareScopeUnspecified
}

// ShareScopeToProto convierte el alcance del dominio.
func ShareScopeToProto(s model.ShareScope) issuesv1.ShareLinkScope {
	switch s {
	case model.ShareScopePlan:
		return issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN
	case model.ShareScopePanel:
		return issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PANEL
	}
	return issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_UNSPECIFIED
}

// ShareDurationFromProto convierte el vencimiento pedido (UNSPECIFIED o
// desconocido -> ShareDurationUnspecified, que el servicio rechaza).
func ShareDurationFromProto(d issuesv1.ShareLinkDuration) model.ShareDuration {
	switch d {
	case issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_SEVEN_DAYS:
		return model.ShareDurationSevenDays
	case issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_THIRTY_DAYS:
		return model.ShareDurationThirtyDays
	case issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_NINETY_DAYS:
		return model.ShareDurationNinetyDays
	case issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_NEVER:
		return model.ShareDurationNever
	}
	return model.ShareDurationUnspecified
}

// optionalTimestamp convierte un *time.Time (nil -> nil).
func optionalTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return TimestampToProto(*t)
}

// ShareLinkToProto convierte un enlace (sin el token ni su hash; tampoco
// created_by_uid, que no sale del servicio).
func ShareLinkToProto(m *model.ShareLink) *issuesv1.ShareLink {
	if m == nil {
		return nil
	}
	return &issuesv1.ShareLink{
		Id:               m.ID,
		ProjectId:        m.ProjectID,
		Scope:            ShareScopeToProto(m.Scope),
		Note:             m.Note,
		CreatedAt:        TimestampToProto(m.CreatedAt),
		ExpiresAt:        optionalTimestamp(m.ExpiresAt),
		LastUsedAt:       optionalTimestamp(m.LastUsedAt),
		RevokedAt:        optionalTimestamp(m.RevokedAt),
		OwnerDisplayName: m.OwnerDisplayName,
	}
}

// ShareLinksToProto convierte un slice de enlaces.
func ShareLinksToProto(links []*model.ShareLink) []*issuesv1.ShareLink {
	out := make([]*issuesv1.ShareLink, len(links))
	for i, l := range links {
		out[i] = ShareLinkToProto(l)
	}
	return out
}

// SharedProjectViewToProto arma la respuesta de GetSharedProject.
func SharedProjectViewToProto(v *model.SharedProjectView) *issuesv1.GetSharedProjectResponse {
	out := &issuesv1.GetSharedProjectResponse{
		Project: &issuesv1.SharedProject{
			Key:         v.Project.Key,
			Name:        v.Project.Name,
			Description: v.Project.Description,
		},
		Scope:    ShareScopeToProto(v.Scope),
		Statuses: make([]*issuesv1.SharedStatus, len(v.Statuses)),
		Labels:   make([]*issuesv1.SharedLabel, len(v.Labels)),
	}
	for i, s := range v.Statuses {
		out.Statuses[i] = &issuesv1.SharedStatus{
			Id:       s.ID,
			Name:     s.Name,
			Category: IssueStateToProto(s.Category),
			Color:    LabelColorToProto(s.Color),
			WipLimit: s.WIPLimit,
			Hidden:   s.Hidden,
		}
	}
	for i, l := range v.Labels {
		out.Labels[i] = &issuesv1.SharedLabel{Id: l.ID, Name: l.Name, Color: LabelColorToProto(l.Color)}
	}
	return out
}

// SharedIssueToProto convierte la vista pública de un issue.
func SharedIssueToProto(m *model.SharedIssue) *issuesv1.SharedIssue {
	if m == nil {
		return nil
	}
	out := &issuesv1.SharedIssue{
		Number:      m.Number,
		Title:       m.Title,
		Description: m.Description,
		WaitingNote: m.WaitingNote,
		State:       IssueStateToProto(m.State),
		Priority:    PriorityToProto(m.Priority),
		StatusId:    m.StatusID,
		LabelIds:    m.LabelIDs,
		CreatedAt:   TimestampToProto(m.CreatedAt),
		UpdatedAt:   TimestampToProto(m.UpdatedAt),
	}
	if a := m.Assignee; a != nil {
		shared := &issuesv1.SharedActor{DisplayName: a.DisplayName, Tool: AgentToolToProto(a.Tool)}
		switch a.Kind {
		case model.ActorKindPerson:
			shared.Kind = issuesv1.ActorKind_ACTOR_KIND_PERSON
		case model.ActorKindAgent:
			shared.Kind = issuesv1.ActorKind_ACTOR_KIND_AGENT
		}
		out.Assignee = shared
	}
	return out
}

// SharedIssuesToProto convierte un slice de issues públicos.
func SharedIssuesToProto(issues []*model.SharedIssue) []*issuesv1.SharedIssue {
	out := make([]*issuesv1.SharedIssue, len(issues))
	for i, it := range issues {
		out[i] = SharedIssueToProto(it)
	}
	return out
}

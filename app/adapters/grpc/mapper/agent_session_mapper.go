package mapper

import (
	"fmt"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/application"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Sesiones de agentes (issue 026). Un valor de enum UNSPECIFIED o
// desconocido queda como un string inválido y la capa de aplicación lo
// rechaza.

var sessionStatusFrom = map[issuesv1.SessionStatus]model.SessionStatus{
	issuesv1.SessionStatus_SESSION_STATUS_WORKING:    model.SessionWorking,
	issuesv1.SessionStatus_SESSION_STATUS_WAITING_OK: model.SessionWaitingOK,
	issuesv1.SessionStatus_SESSION_STATUS_IDLE:       model.SessionIdle,
	issuesv1.SessionStatus_SESSION_STATUS_ENDED:      model.SessionEnded,
	issuesv1.SessionStatus_SESSION_STATUS_UNKNOWN:    model.SessionUnknown,
	issuesv1.SessionStatus_SESSION_STATUS_STALLED:    model.SessionStalled,
}

var waitingOnFrom = map[issuesv1.WaitingOn]model.WaitingOn{
	issuesv1.WaitingOn_WAITING_ON_UNSPECIFIED: "",
	issuesv1.WaitingOn_WAITING_ON_NONE:        model.WaitingNone,
	issuesv1.WaitingOn_WAITING_ON_PERSON:      model.WaitingPerson,
	issuesv1.WaitingOn_WAITING_ON_SESSION:     model.WaitingSession,
	issuesv1.WaitingOn_WAITING_ON_OTHER:       model.WaitingOther,
}

var sessionLinkFrom = map[issuesv1.SessionLink]model.SessionLink{
	issuesv1.SessionLink_SESSION_LINK_NONE:       model.SessionLinkNone,
	issuesv1.SessionLink_SESSION_LINK_AGENT_NAME: model.SessionLinkAgentName,
	issuesv1.SessionLink_SESSION_LINK_WORKSPACE:  model.SessionLinkWorkspace,
	issuesv1.SessionLink_SESSION_LINK_CWD:        model.SessionLinkCwd,
}

// SessionStatusFromProto convierte un estado.
func SessionStatusFromProto(s issuesv1.SessionStatus) model.SessionStatus {
	if v, ok := sessionStatusFrom[s]; ok {
		return v
	}
	return model.SessionStatus(fmt.Sprintf("unknown(%d)", int32(s)))
}

// SessionStatusToProto convierte un estado del dominio.
func SessionStatusToProto(s model.SessionStatus) issuesv1.SessionStatus {
	for k, v := range sessionStatusFrom {
		if v == s {
			return k
		}
	}
	return issuesv1.SessionStatus_SESSION_STATUS_UNSPECIFIED
}

// WaitingOnToProto convierte qué espera una sesión.
func WaitingOnToProto(w model.WaitingOn) issuesv1.WaitingOn {
	for k, v := range waitingOnFrom {
		if v == w && v != "" {
			return k
		}
	}
	return issuesv1.WaitingOn_WAITING_ON_UNSPECIFIED
}

func sessionLinkToProto(l model.SessionLink) issuesv1.SessionLink {
	for k, v := range sessionLinkFrom {
		if v == l {
			return k
		}
	}
	return issuesv1.SessionLink_SESSION_LINK_UNSPECIFIED
}

func sessionSourceToProto(s model.SessionSource) issuesv1.SessionSource {
	if s == model.SessionSourceHerdr {
		return issuesv1.SessionSource_SESSION_SOURCE_HERDR
	}
	return issuesv1.SessionSource_SESSION_SOURCE_UNSPECIFIED
}

// ReportInputFromProto convierte un ReportSessionsRequest.
func ReportInputFromProto(req *issuesv1.ReportSessionsRequest) application.ReportInput {
	in := application.ReportInput{Machine: req.GetMachine()}
	if req.GetSource() == issuesv1.SessionSource_SESSION_SOURCE_HERDR {
		in.Source = model.SessionSourceHerdr
	} else {
		in.Source = model.SessionSource(fmt.Sprintf("unknown(%d)", int32(req.GetSource())))
	}
	in.Sessions = make([]model.ReportedSession, len(req.GetSessions()))
	for i, s := range req.GetSessions() {
		waiting, ok := waitingOnFrom[s.GetWaitingOn()]
		if !ok {
			waiting = model.WaitingOn(fmt.Sprintf("unknown(%d)", int32(s.GetWaitingOn())))
		}
		link, ok := sessionLinkFrom[s.GetLink()]
		if !ok {
			link = model.SessionLink(fmt.Sprintf("unknown(%d)", int32(s.GetLink())))
		}
		in.Sessions[i] = model.ReportedSession{
			ExternalID:     s.GetExternalId(),
			Tool:           AgentToolFromProto(s.GetTool()),
			CwdRel:         s.GetCwdRel(),
			SessionName:    s.GetSessionName(),
			WorkspaceLabel: s.GetWorkspaceLabel(),
			ProjectKey:     s.GetProjectKey(),
			IssueNumber:    s.GetIssueNumber(),
			Link:           link,
			Status:         SessionStatusFromProto(s.GetStatus()),
			WaitingOn:      waiting,
			WaitingTarget:  s.GetWaitingTarget(),
			WaitingText:    s.GetWaitingText(),
		}
	}
	return in
}

// ReportResultToProto convierte el resultado de un reporte.
func ReportResultToProto(r *application.ReportResult) *issuesv1.ReportSessionsResponse {
	out := &issuesv1.ReportSessionsResponse{
		Created:   int32(r.Created),
		Updated:   int32(r.Updated),
		Unchanged: int32(r.Unchanged),
		Ended:     int32(r.Ended),
	}
	for _, rj := range r.Rejected {
		out.Rejected = append(out.Rejected, &issuesv1.SessionRejection{ExternalId: rj.ExternalID, Saved: rj.Saved, Reason: rj.Reason})
	}
	return out
}

// ListSessionsInputFromProto convierte un ListAgentSessionsRequest.
func ListSessionsInputFromProto(req *issuesv1.ListAgentSessionsRequest) application.ListSessionsInput {
	in := application.ListSessionsInput{
		ProjectID:       req.GetProjectId(),
		IssueNumber:     req.GetIssueNumber(),
		AgentActorID:    req.GetAgentActorId(),
		IncludeInactive: req.GetIncludeInactive(),
		Unlinked:        req.GetUnlinked(),
	}
	in.Page.Cursor = req.GetPage().GetCursor()
	in.Page.Limit = req.GetPage().GetLimit()
	for _, s := range req.GetStatuses() {
		in.Statuses = append(in.Statuses, SessionStatusFromProto(s))
	}
	return in
}

// AgentSessionToProto convierte una sesión (con su estado efectivo).
func AgentSessionToProto(m *model.AgentSession) *issuesv1.AgentSession {
	out := &issuesv1.AgentSession{
		Id:               m.ID,
		Source:           sessionSourceToProto(m.Source),
		ExternalId:       m.ExternalID,
		AgentActorId:     m.AgentActorID,
		AgentDisplayName: m.AgentDisplayName,
		Tool:             AgentToolToProto(m.Tool),
		Machine:          m.Machine,
		CwdRel:           m.CwdRel,
		SessionName:      m.SessionName,
		WorkspaceLabel:   m.WorkspaceLabel,
		ProjectId:        m.ProjectID,
		IssueNumber:      m.IssueNumber,
		Link:             sessionLinkToProto(m.Link),
		Status:           SessionStatusToProto(m.Status),
		StatusChangedAt:  TimestampToProto(m.StatusChangedAt),
		LastSeenAt:       TimestampToProto(m.LastSeenAt),
		StartedAt:        TimestampToProto(m.StartedAt),
		WaitingOn:        WaitingOnToProto(m.WaitingOn),
		WaitingTarget:    m.WaitingTarget,
		WaitingText:      m.WaitingText,
	}
	if m.EndedAt != nil {
		out.EndedAt = TimestampToProto(*m.EndedAt)
	}
	return out
}

// AgentSessionsToProto convierte una lista de sesiones.
func AgentSessionsToProto(list []*model.AgentSession) []*issuesv1.AgentSession {
	out := make([]*issuesv1.AgentSession, len(list))
	for i, s := range list {
		out[i] = AgentSessionToProto(s)
	}
	return out
}

// IssueSessionsToProto convierte el resumen de un issue (nil = ausente).
func IssueSessionsToProto(m *model.IssueSessions) *issuesv1.IssueSessions {
	if m == nil {
		return nil
	}
	out := &issuesv1.IssueSessions{
		Active:      int32(m.Active),
		Working:     int32(m.Working),
		Waiting:     int32(m.Waiting),
		Stalled:     int32(m.Stalled),
		Idle:        int32(m.Idle),
		NeedsPerson: m.NeedsPerson,
	}
	for _, s := range m.Items {
		out.Items = append(out.Items, &issuesv1.SessionBrief{
			SessionId:        s.ID,
			AgentActorId:     s.AgentActorID,
			AgentDisplayName: s.AgentDisplayName,
			SessionName:      s.SessionName,
			Machine:          s.Machine,
			Status:           SessionStatusToProto(s.Status),
			WaitingOn:        WaitingOnToProto(s.WaitingOn),
			WaitingTarget:    s.WaitingTarget,
			StatusChangedAt:  TimestampToProto(s.StatusChangedAt),
		})
	}
	return out
}

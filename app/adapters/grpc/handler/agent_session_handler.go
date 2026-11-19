package handler

import (
	"context"

	"go.uber.org/zap"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/adapters/grpc/mapper"
	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// AgentSessionHandler implementa issuesv1.AgentSessionServiceServer (issue
// 026). Mismo patrón que IssueHandler.
type AgentSessionHandler struct {
	issuesv1.UnimplementedAgentSessionServiceServer

	logger *zap.Logger
	svc    *application.AgentSessionService
}

var _ issuesv1.AgentSessionServiceServer = (*AgentSessionHandler)(nil)

// NewAgentSessionHandler construye el handler.
func NewAgentSessionHandler(logger *zap.Logger, svc *application.AgentSessionService) *AgentSessionHandler {
	return &AgentSessionHandler{logger: logger, svc: svc}
}

// ReportSessions: solo una persona (un token de agente → PERMISSION_DENIED).
// El permiso sessions:write lo exige el gateway.
func (h *AgentSessionHandler) ReportSessions(ctx context.Context, req *issuesv1.ReportSessionsRequest) (*issuesv1.ReportSessionsResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	if agentIDFrom(ctx) != "" {
		return nil, domainerrors.MapToGRPC(domainerrors.PermissionDeniedError("only a person token can report sessions", map[string]any{"metadata": MetadataAgentID}), h.logger)
	}
	res, err := h.svc.Report(ctx, uid, mapper.ReportInputFromProto(req))
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return mapper.ReportResultToProto(res), nil
}

func (h *AgentSessionHandler) ListAgentSessions(ctx context.Context, req *issuesv1.ListAgentSessionsRequest) (*issuesv1.ListAgentSessionsResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	page, err := h.svc.List(ctx, uid, mapper.ListSessionsInputFromProto(req))
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.ListAgentSessionsResponse{
		Sessions: mapper.AgentSessionsToProto(page.Sessions),
		Page:     &issuesv1.PageResponse{NextCursor: page.NextCursor, HasNext: page.HasNext},
	}, nil
}

// WithSessions conecta el resumen de sesiones (Issue.sessions, issue 026).
// Sin llamarlo, los issues salen sin resumen (tests que no lo usan).
func (h *IssueHandler) WithSessions(svc *application.AgentSessionService) *IssueHandler {
	h.sessionSvc = svc
	return h
}

// attachSessions llena Issue.sessions de issues (todos del mismo proyecto)
// con una consulta. withItems solo en GetIssue; las demás respuestas llevan
// solo los contadores.
func (h *IssueHandler) attachSessions(ctx context.Context, withItems bool, issues ...*issuesv1.Issue) error {
	if h.sessionSvc == nil || len(issues) == 0 {
		return nil
	}
	numbers := make([]int32, len(issues))
	for i, is := range issues {
		numbers[i] = is.GetNumber()
	}
	sums, err := h.sessionSvc.Summaries(ctx, issues[0].GetProjectId(), numbers, withItems)
	if err != nil {
		return err
	}
	for _, is := range issues {
		is.Sessions = mapper.IssueSessionsToProto(sums[is.GetNumber()])
	}
	return nil
}

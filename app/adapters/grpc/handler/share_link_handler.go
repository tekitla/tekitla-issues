package handler

import (
	"context"

	"go.uber.org/zap"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/adapters/grpc/mapper"
	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// ShareLinkHandler implementa issuesv1.ShareLinkServiceServer (issue 009):
// gestión de los enlaces de solo lectura. Solo una persona (requirePerson):
// un agente no crea ni revoca enlaces, igual que no crea tokens.
type ShareLinkHandler struct {
	issuesv1.UnimplementedShareLinkServiceServer

	logger *zap.Logger
	svc    *application.ShareLinkService
}

var _ issuesv1.ShareLinkServiceServer = (*ShareLinkHandler)(nil)

// NewShareLinkHandler construye el handler.
func NewShareLinkHandler(logger *zap.Logger, svc *application.ShareLinkService) *ShareLinkHandler {
	return &ShareLinkHandler{logger: logger, svc: svc}
}

func (h *ShareLinkHandler) CreateShareLink(ctx context.Context, req *issuesv1.CreateShareLinkRequest) (*issuesv1.CreateShareLinkResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	link, token, err := h.svc.CreateShareLink(ctx, uid, application.CreateShareLinkInput{
		ProjectID:        req.GetProjectId(),
		Scope:            mapper.ShareScopeFromProto(req.GetScope()),
		Duration:         mapper.ShareDurationFromProto(req.GetDuration()),
		Note:             req.GetNote(),
		OwnerDisplayName: req.GetOwnerDisplayName(),
	})
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.CreateShareLinkResponse{ShareLink: mapper.ShareLinkToProto(link), Token: token}, nil
}

func (h *ShareLinkHandler) ListShareLinks(ctx context.Context, req *issuesv1.ListShareLinksRequest) (*issuesv1.ListShareLinksResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	links, err := h.svc.ListShareLinks(ctx, uid, req.GetProjectId())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.ListShareLinksResponse{ShareLinks: mapper.ShareLinksToProto(links)}, nil
}

func (h *ShareLinkHandler) RevokeShareLink(ctx context.Context, req *issuesv1.RevokeShareLinkRequest) (*issuesv1.RevokeShareLinkResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	link, err := h.svc.RevokeShareLink(ctx, uid, req.GetProjectId(), req.GetShareLinkId())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.RevokeShareLinkResponse{ShareLink: mapper.ShareLinkToProto(link)}, nil
}

// SharedProjectHandler implementa issuesv1.SharedProjectServiceServer (issue
// 009): lectura pública por el token de un enlace. No lee x-user-id ni
// x-agent-id: con o sin identidad, la respuesta es la misma.
type SharedProjectHandler struct {
	issuesv1.UnimplementedSharedProjectServiceServer

	logger *zap.Logger
	svc    *application.ShareLinkService
}

var _ issuesv1.SharedProjectServiceServer = (*SharedProjectHandler)(nil)

// NewSharedProjectHandler construye el handler.
func NewSharedProjectHandler(logger *zap.Logger, svc *application.ShareLinkService) *SharedProjectHandler {
	return &SharedProjectHandler{logger: logger, svc: svc}
}

func (h *SharedProjectHandler) GetSharedProject(ctx context.Context, req *issuesv1.GetSharedProjectRequest) (*issuesv1.GetSharedProjectResponse, error) {
	view, err := h.svc.GetSharedProject(ctx, req.GetToken())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return mapper.SharedProjectViewToProto(view), nil
}

func (h *SharedProjectHandler) ListSharedIssues(ctx context.Context, req *issuesv1.ListSharedIssuesRequest) (*issuesv1.ListSharedIssuesResponse, error) {
	issues, next, hasNext, err := h.svc.ListSharedIssues(ctx, req.GetToken(), mapper.PageFilterFromProto(req.GetPage()))
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.ListSharedIssuesResponse{
		Issues: mapper.SharedIssuesToProto(issues),
		Page:   mapper.PageResponseToProto(next, hasNext),
	}, nil
}

func (h *SharedProjectHandler) GetSharedIssue(ctx context.Context, req *issuesv1.GetSharedIssueRequest) (*issuesv1.GetSharedIssueResponse, error) {
	issue, err := h.svc.GetSharedIssue(ctx, req.GetToken(), req.GetNumber())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.GetSharedIssueResponse{Issue: mapper.SharedIssueToProto(issue)}, nil
}

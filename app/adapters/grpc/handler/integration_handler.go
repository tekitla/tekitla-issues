package handler

import (
	"context"

	"go.uber.org/zap"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/adapters/grpc/mapper"
	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// RepositoryHandler implementa issuesv1.RepositoryServiceServer (issue 016).
// Gestionar conexiones y links es de una persona (requirePerson); leer links
// también lo puede hacer un agente (issues:read, para el MCP y tk).
type RepositoryHandler struct {
	issuesv1.UnimplementedRepositoryServiceServer

	logger *zap.Logger
	svc    *application.IntegrationService
}

var _ issuesv1.RepositoryServiceServer = (*RepositoryHandler)(nil)

// NewRepositoryHandler construye el handler.
func NewRepositoryHandler(logger *zap.Logger, svc *application.IntegrationService) *RepositoryHandler {
	return &RepositoryHandler{logger: logger, svc: svc}
}

func (h *RepositoryHandler) fail(err error) error { return domainerrors.MapToGRPC(err, h.logger) }

func (h *RepositoryHandler) ListConnections(ctx context.Context, req *issuesv1.ListConnectionsRequest) (*issuesv1.ListConnectionsResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, h.fail(err)
	}
	view, err := h.svc.ListConnections(ctx, uid, req.GetProjectId())
	if err != nil {
		return nil, h.fail(err)
	}
	return &issuesv1.ListConnectionsResponse{
		Connections:      mapper.ConnectionsToProto(view.Connections),
		GithubInstallUrl: view.GitHubInstallURL,
		GitlabEnabled:    view.GitLabEnabled,
	}, nil
}

func (h *RepositoryHandler) ConnectOrganization(ctx context.Context, req *issuesv1.ConnectOrganizationRequest) (*issuesv1.ConnectOrganizationResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, h.fail(err)
	}
	in := application.ConnectInput{
		ProjectID: req.GetProjectId(),
		Scope:     mapper.ScopeFromProto(req.GetScope()),
		Topic:     req.GetTopic(),
		Selected:  req.GetSelected(),
	}
	switch p := req.GetProvider().(type) {
	case *issuesv1.ConnectOrganizationRequest_Github:
		in.Provider = model.RepoProviderGitHub
		in.InstallationID = p.Github.GetInstallationId()
	case *issuesv1.ConnectOrganizationRequest_Gitlab:
		in.Provider = model.RepoProviderGitLab
	}
	conn, repos, err := h.svc.ConnectOrganization(ctx, uid, in)
	if err != nil {
		return nil, h.fail(err)
	}
	return &issuesv1.ConnectOrganizationResponse{
		Connection:   mapper.ConnectionToProto(conn),
		Repositories: mapper.RepositoriesToProto(repos),
	}, nil
}

func (h *RepositoryHandler) UpdateConnection(ctx context.Context, req *issuesv1.UpdateConnectionRequest) (*issuesv1.UpdateConnectionResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, h.fail(err)
	}
	conn, repos, err := h.svc.UpdateConnection(ctx, uid, application.UpdateConnectionInput{
		ProjectID:    req.GetProjectId(),
		ConnectionID: req.GetConnectionId(),
		Scope:        mapper.ScopeFromProto(req.GetScope()),
		Topic:        req.GetTopic(),
		Selected:     req.GetSelected(),
	})
	if err != nil {
		return nil, h.fail(err)
	}
	return &issuesv1.UpdateConnectionResponse{
		Connection:   mapper.ConnectionToProto(conn),
		Repositories: mapper.RepositoriesToProto(repos),
	}, nil
}

func (h *RepositoryHandler) DisconnectOrganization(ctx context.Context, req *issuesv1.DisconnectOrganizationRequest) (*issuesv1.DisconnectOrganizationResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, h.fail(err)
	}
	if err := h.svc.DisconnectOrganization(ctx, uid, req.GetProjectId(), req.GetConnectionId()); err != nil {
		return nil, h.fail(err)
	}
	return &issuesv1.DisconnectOrganizationResponse{}, nil
}

func (h *RepositoryHandler) SyncRepositories(ctx context.Context, req *issuesv1.SyncRepositoriesRequest) (*issuesv1.SyncRepositoriesResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, h.fail(err)
	}
	res, err := h.svc.SyncRepositories(ctx, uid, req.GetProjectId(), req.GetConnectionId())
	if err != nil {
		return nil, h.fail(err)
	}
	return &issuesv1.SyncRepositoriesResponse{
		Connection:   mapper.ConnectionToProto(res.Connection),
		Repositories: mapper.RepositoriesToProto(res.Repositories),
		LinksUpdated: int32(res.LinksUpdated),
	}, nil
}

func (h *RepositoryHandler) ListRepositories(ctx context.Context, req *issuesv1.ListRepositoriesRequest) (*issuesv1.ListRepositoriesResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, h.fail(err)
	}
	repos, err := h.svc.ListRepositories(ctx, uid, req.GetProjectId())
	if err != nil {
		return nil, h.fail(err)
	}
	return &issuesv1.ListRepositoriesResponse{Repositories: mapper.RepositoriesToProto(repos)}, nil
}

func (h *RepositoryHandler) ListIssueLinks(ctx context.Context, req *issuesv1.ListIssueLinksRequest) (*issuesv1.ListIssueLinksResponse, error) {
	// Lectura: también un agente (el gateway exige issues:read).
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, h.fail(err)
	}
	links, err := h.svc.ListIssueLinks(ctx, uid, req.GetProjectId(), req.GetIssueNumber())
	if err != nil {
		return nil, h.fail(err)
	}
	return &issuesv1.ListIssueLinksResponse{Links: mapper.IssueLinksToProto(links)}, nil
}

func (h *RepositoryHandler) LinkIssue(ctx context.Context, req *issuesv1.LinkIssueRequest) (*issuesv1.LinkIssueResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, h.fail(err)
	}
	link, err := h.svc.LinkIssue(ctx, uid, req.GetProjectId(), req.GetNumber(), req.GetRepositoryId(), req.GetBranch())
	if err != nil {
		return nil, h.fail(err)
	}
	return &issuesv1.LinkIssueResponse{Link: mapper.IssueLinkToProto(link)}, nil
}

func (h *RepositoryHandler) UnlinkIssue(ctx context.Context, req *issuesv1.UnlinkIssueRequest) (*issuesv1.UnlinkIssueResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, h.fail(err)
	}
	if err := h.svc.UnlinkIssue(ctx, uid, req.GetProjectId(), req.GetNumber(), req.GetLinkId()); err != nil {
		return nil, h.fail(err)
	}
	return &issuesv1.UnlinkIssueResponse{}, nil
}

// WebhookHandler implementa issuesv1.WebhookServiceServer (issue 016). Es
// interno: no lee x-user-id (la autenticación es la firma del webhook).
type WebhookHandler struct {
	issuesv1.UnimplementedWebhookServiceServer

	logger *zap.Logger
	svc    *application.IntegrationService
}

var _ issuesv1.WebhookServiceServer = (*WebhookHandler)(nil)

// NewWebhookHandler construye el handler.
func NewWebhookHandler(logger *zap.Logger, svc *application.IntegrationService) *WebhookHandler {
	return &WebhookHandler{logger: logger, svc: svc}
}

func (h *WebhookHandler) ReceiveWebhook(ctx context.Context, req *issuesv1.ReceiveWebhookRequest) (*issuesv1.ReceiveWebhookResponse, error) {
	outcome, err := h.svc.ReceiveWebhook(ctx, application.WebhookInput{
		Provider:   mapper.ProviderFromProto(req.GetProvider()),
		Event:      req.GetEvent(),
		DeliveryID: req.GetDeliveryId(),
		Signature:  req.GetSignature(),
		Token:      req.GetToken(),
		Body:       req.GetBody(),
	})
	if err != nil {
		// Sin logger: un 401 de un webhook ajeno no es un error del servicio,
		// y sus detalles no deben quedar en el log.
		return nil, domainerrors.MapToGRPC(err, nil)
	}
	h.logger.Info("webhook", zap.String("provider", string(mapper.ProviderFromProto(req.GetProvider()))),
		zap.String("event", req.GetEvent()), zap.String("delivery_id", req.GetDeliveryId()), zap.Int("outcome", int(outcome)))
	out := issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED
	switch outcome {
	case application.WebhookDuplicate:
		out = issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_DUPLICATE
	case application.WebhookIgnored:
		out = issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_IGNORED
	}
	return &issuesv1.ReceiveWebhookResponse{Outcome: out}, nil
}

package handler

import (
	"context"
	"time"

	"go.uber.org/zap"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/adapters/grpc/mapper"
	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// ActorHandler implementa issuesv1.ActorServiceServer (issue 025): agentes y
// tokens de la persona del usuario. Mismo patrón que IssueHandler.
type ActorHandler struct {
	issuesv1.UnimplementedActorServiceServer

	logger *zap.Logger
	svc    *application.ActorService
}

var _ issuesv1.ActorServiceServer = (*ActorHandler)(nil)

// NewActorHandler construye el handler.
func NewActorHandler(logger *zap.Logger, svc *application.ActorService) *ActorHandler {
	return &ActorHandler{logger: logger, svc: svc}
}

// requirePerson exige x-user-id y rechaza a un agente: un token nunca crea
// agentes ni otros tokens (ADR 0009). El gateway ya no deja pasar tokens a
// estas rutas; esto es la segunda barrera.
func requirePerson(ctx context.Context) (string, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return "", err
	}
	if agentIDFrom(ctx) != "" {
		return "", domainerrors.PermissionDeniedError("agents cannot manage actors or tokens", map[string]any{"metadata": MetadataAgentID})
	}
	return uid, nil
}

func (h *ActorHandler) ListActors(ctx context.Context, req *issuesv1.ListActorsRequest) (*issuesv1.ListActorsResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	actors, err := h.svc.ListActors(ctx, uid, req.GetIncludeRevoked())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.ListActorsResponse{Actors: mapper.ActorsToProto(actors)}, nil
}

func (h *ActorHandler) CreateAgent(ctx context.Context, req *issuesv1.CreateAgentRequest) (*issuesv1.CreateAgentResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	agent, err := h.svc.CreateAgent(ctx, uid, application.CreateAgentInput{
		DisplayName:  req.GetDisplayName(),
		Tool:         mapper.AgentToolFromProto(req.GetTool()),
		OwnerActorID: req.GetOwnerActorId(),
	})
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.CreateAgentResponse{Agent: mapper.ActorToProto(agent)}, nil
}

func (h *ActorHandler) RevokeActor(ctx context.Context, req *issuesv1.RevokeActorRequest) (*issuesv1.RevokeActorResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	actor, err := h.svc.RevokeActor(ctx, uid, req.GetActorId())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.RevokeActorResponse{Actor: mapper.ActorToProto(actor)}, nil
}

func (h *ActorHandler) CreateToken(ctx context.Context, req *issuesv1.CreateTokenRequest) (*issuesv1.CreateTokenResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	in := application.CreateTokenInput{
		ActorID: req.GetActorId(),
		Name:    req.GetName(),
		Scopes:  mapper.TokenScopesFromProto(req.GetScopes()),
	}
	if req.ExpiresAt != nil {
		if err := req.GetExpiresAt().CheckValid(); err != nil {
			return nil, domainerrors.MapToGRPC(domainerrors.ValidationError("invalid expires_at", map[string]any{"field": "expires_at"}), h.logger)
		}
		t := req.GetExpiresAt().AsTime().UTC().Truncate(time.Microsecond)
		in.ExpiresAt = &t
	}
	token, secret, err := h.svc.CreateToken(ctx, uid, in)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.CreateTokenResponse{Token: mapper.TokenToProto(token), Secret: secret}, nil
}

func (h *ActorHandler) ListTokens(ctx context.Context, req *issuesv1.ListTokensRequest) (*issuesv1.ListTokensResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	tokens, err := h.svc.ListTokens(ctx, uid, req.GetActorId(), req.GetIncludeInactive())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.ListTokensResponse{Tokens: mapper.TokensToProto(tokens)}, nil
}

func (h *ActorHandler) RevokeToken(ctx context.Context, req *issuesv1.RevokeTokenRequest) (*issuesv1.RevokeTokenResponse, error) {
	uid, err := requirePerson(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	token, err := h.svc.RevokeToken(ctx, uid, req.GetTokenId())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.RevokeTokenResponse{Token: mapper.TokenToProto(token)}, nil
}

// TokenAuthHandler implementa issuesv1.TokenAuthServiceServer: el RPC
// interno con el que el gateway resuelve un `tka_…`. No exige x-user-id (el
// gateway todavía no sabe quién es). El token nunca va a los logs: el
// interceptor no registra requests.
type TokenAuthHandler struct {
	issuesv1.UnimplementedTokenAuthServiceServer

	logger *zap.Logger
	svc    *application.ActorService
}

var _ issuesv1.TokenAuthServiceServer = (*TokenAuthHandler)(nil)

// NewTokenAuthHandler construye el handler.
func NewTokenAuthHandler(logger *zap.Logger, svc *application.ActorService) *TokenAuthHandler {
	return &TokenAuthHandler{logger: logger, svc: svc}
}

func (h *TokenAuthHandler) ResolveToken(ctx context.Context, req *issuesv1.ResolveTokenRequest) (*issuesv1.ResolveTokenResponse, error) {
	resolved, err := h.svc.ResolveToken(ctx, req.GetToken())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return mapper.ResolvedTokenToProto(resolved), nil
}

package mapper

import (
	"fmt"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// ActorToProto convierte un actor del dominio a su mensaje proto.
func ActorToProto(m *model.Actor) *issuesv1.Actor {
	if m == nil {
		return nil
	}
	out := &issuesv1.Actor{
		Id:           m.ID,
		DisplayName:  m.DisplayName,
		OwnerActorId: m.OwnerActorID,
		Tool:         AgentToolToProto(m.Tool),
		CreatedAt:    TimestampToProto(m.CreatedAt),
		UserId:       m.UserUID,
	}
	switch m.Kind {
	case model.ActorKindPerson:
		out.Kind = issuesv1.ActorKind_ACTOR_KIND_PERSON
	case model.ActorKindAgent:
		out.Kind = issuesv1.ActorKind_ACTOR_KIND_AGENT
	}
	if m.RevokedAt != nil {
		out.RevokedAt = TimestampToProto(*m.RevokedAt)
	}
	return out
}

// ActorsToProto convierte un slice de actores.
func ActorsToProto(actors []*model.Actor) []*issuesv1.Actor {
	out := make([]*issuesv1.Actor, len(actors))
	for i, a := range actors {
		out[i] = ActorToProto(a)
	}
	return out
}

// AgentToolFromProto convierte la herramienta de un agente. UNSPECIFIED y
// valores desconocidos quedan inválidos y la capa de aplicación los rechaza.
func AgentToolFromProto(t issuesv1.AgentTool) model.AgentTool {
	switch t {
	case issuesv1.AgentTool_AGENT_TOOL_UNSPECIFIED:
		return model.AgentToolUnspecified
	case issuesv1.AgentTool_AGENT_TOOL_CLAUDE_CODE:
		return model.AgentToolClaudeCode
	case issuesv1.AgentTool_AGENT_TOOL_CODEX:
		return model.AgentToolCodex
	case issuesv1.AgentTool_AGENT_TOOL_CLOUD:
		return model.AgentToolCloud
	case issuesv1.AgentTool_AGENT_TOOL_OTHER:
		return model.AgentToolOther
	}
	return model.AgentTool(fmt.Sprintf("unknown(%d)", int32(t)))
}

// AgentToolToProto convierte la herramienta del dominio al enum proto.
func AgentToolToProto(t model.AgentTool) issuesv1.AgentTool {
	switch t {
	case model.AgentToolClaudeCode:
		return issuesv1.AgentTool_AGENT_TOOL_CLAUDE_CODE
	case model.AgentToolCodex:
		return issuesv1.AgentTool_AGENT_TOOL_CODEX
	case model.AgentToolCloud:
		return issuesv1.AgentTool_AGENT_TOOL_CLOUD
	case model.AgentToolOther:
		return issuesv1.AgentTool_AGENT_TOOL_OTHER
	}
	return issuesv1.AgentTool_AGENT_TOOL_UNSPECIFIED
}

// TokenScopesFromProto convierte los scopes pedidos. UNSPECIFIED y valores
// desconocidos quedan inválidos y la capa de aplicación los rechaza.
func TokenScopesFromProto(scopes []issuesv1.TokenScope) []model.TokenScope {
	out := make([]model.TokenScope, len(scopes))
	for i, s := range scopes {
		switch s {
		case issuesv1.TokenScope_TOKEN_SCOPE_ISSUES_READ:
			out[i] = model.TokenScopeIssuesRead
		case issuesv1.TokenScope_TOKEN_SCOPE_ISSUES_WRITE:
			out[i] = model.TokenScopeIssuesWrite
		default:
			out[i] = model.TokenScope(fmt.Sprintf("unknown(%d)", int32(s)))
		}
	}
	return out
}

// TokenScopesToProto convierte los scopes del dominio al enum proto.
func TokenScopesToProto(scopes []model.TokenScope) []issuesv1.TokenScope {
	out := make([]issuesv1.TokenScope, 0, len(scopes))
	for _, s := range scopes {
		switch s {
		case model.TokenScopeIssuesRead:
			out = append(out, issuesv1.TokenScope_TOKEN_SCOPE_ISSUES_READ)
		case model.TokenScopeIssuesWrite:
			out = append(out, issuesv1.TokenScope_TOKEN_SCOPE_ISSUES_WRITE)
		}
	}
	return out
}

// TokenToProto convierte un token (nunca lleva el secreto ni el hash: el
// dominio no los tiene).
func TokenToProto(m *model.Token) *issuesv1.Token {
	if m == nil {
		return nil
	}
	out := &issuesv1.Token{
		Id:        m.ID,
		ActorId:   m.ActorID,
		Name:      m.Name,
		Scopes:    TokenScopesToProto(m.Scopes),
		CreatedAt: TimestampToProto(m.CreatedAt),
		ExpiresAt: TimestampToProto(m.ExpiresAt),
	}
	if m.LastUsedAt != nil {
		out.LastUsedAt = TimestampToProto(*m.LastUsedAt)
	}
	if m.RevokedAt != nil {
		out.RevokedAt = TimestampToProto(*m.RevokedAt)
	}
	return out
}

// TokensToProto convierte un slice de tokens.
func TokensToProto(tokens []*model.Token) []*issuesv1.Token {
	out := make([]*issuesv1.Token, len(tokens))
	for i, t := range tokens {
		out[i] = TokenToProto(t)
	}
	return out
}

// ResolvedTokenToProto convierte la identidad de un token.
func ResolvedTokenToProto(m *model.ResolvedToken) *issuesv1.ResolveTokenResponse {
	return &issuesv1.ResolveTokenResponse{
		UserId:        m.UserUID,
		AgentId:       m.AgentID,
		Scopes:        TokenScopesToProto(m.Scopes),
		ExpiresAt:     TimestampToProto(m.ExpiresAt),
		TokenId:       m.TokenID,
		PersonActorId: m.PersonActorID,
	}
}

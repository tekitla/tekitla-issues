package model

import (
	"slices"
	"strings"
	"time"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// Author es quién hace un cambio: la persona responsable (su uid, el
// x-user-id del gateway) y, si lo hizo uno de sus agentes, el id del agente
// (x-agent-id). En el historial, UserID va en actor_id y AgentID en agent_id.
type Author struct {
	UserID  string
	AgentID string
}

// ActorKind es el tipo de actor. El string es el que se guarda en
// issues.actors.kind.
type ActorKind string

// Tipos de actor.
const (
	ActorKindPerson ActorKind = "person"
	ActorKindAgent  ActorKind = "agent"
)

// AgentTool es la herramienta con la que trabaja un agente. El string es el
// que se guarda en issues.actors.tool; vacío en una persona.
type AgentTool string

// Herramientas de un agente.
const (
	AgentToolUnspecified AgentTool = ""
	AgentToolClaudeCode  AgentTool = "claude_code"
	AgentToolCodex       AgentTool = "codex"
	AgentToolCloud       AgentTool = "cloud"
	AgentToolOther       AgentTool = "other"
)

// IsValid informa si la herramienta es una de las definidas (la vacía no lo
// es).
func (t AgentTool) IsValid() bool {
	switch t {
	case AgentToolClaudeCode, AgentToolCodex, AgentToolCloud, AgentToolOther:
		return true
	}
	return false
}

// AgentNameMaxLen es el largo máximo del nombre de un agente.
const AgentNameMaxLen = 80

// Actor es quien hace el trabajo: una persona (ligada a su uid de Firebase)
// o un agente de una persona.
type Actor struct {
	ID   string
	Kind ActorKind
	// UserUID: solo en personas.
	UserUID string
	// OwnerActorID y Tool: solo en agentes.
	OwnerActorID string
	Tool         AgentTool
	// DisplayName: vacío en una persona (el servicio no conoce su nombre).
	DisplayName string
	CreatedAt   time.Time
	// RevokedAt nil = no revocado.
	RevokedAt *time.Time
}

// IsRevoked informa si el actor está revocado.
func (a *Actor) IsRevoked() bool { return a.RevokedAt != nil }

// NormalizeAgentName recorta espacios y valida el nombre de un agente (1 a
// AgentNameMaxLen runas).
func NormalizeAgentName(raw string) (string, error) {
	return requiredText("display_name", raw, AgentNameMaxLen)
}

// TokenScope es el alcance de un token. El string es el que se guarda en
// issues.tokens.scopes y el que el gateway propaga en x-roles.
type TokenScope string

// Alcances de un token. Write no implica read.
const (
	TokenScopeIssuesRead  TokenScope = "issues:read"
	TokenScopeIssuesWrite TokenScope = "issues:write"
)

// tokenScopeOrder es el orden del enum proto: los scopes se guardan y se
// devuelven así.
var tokenScopeOrder = []TokenScope{TokenScopeIssuesRead, TokenScopeIssuesWrite}

// IsValid informa si el scope es uno de los definidos.
func (s TokenScope) IsValid() bool { return slices.Contains(tokenScopeOrder, s) }

// Límites y formato de los tokens (ADR 0009).
const (
	// TokenPrefix va delante de los 32 bytes aleatorios en base64url: sirve
	// para secret scanning y para reconocerlo en el gateway.
	TokenPrefix = "tka_"
	// TokenSecretBytes es la entropía del token.
	TokenSecretBytes = 32
	// TokenNameMaxLen es el largo máximo del nombre de un token.
	TokenNameMaxLen = 80
	// TokenDefaultTTL es el vencimiento si no se pide uno.
	TokenDefaultTTL = 30 * 24 * time.Hour
	// TokenMaxTTL es el vencimiento máximo.
	TokenMaxTTL = 90 * 24 * time.Hour
)

// Token es un token emitido a nombre de un actor. Nunca lleva el secreto: se
// guarda solo su hash (TokenHash, que tampoco sale del servicio).
type Token struct {
	ID         string
	ActorID    string
	Name       string
	Scopes     []TokenScope
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// IsActive informa si el token no está revocado ni vencido en now (no mira
// a su actor).
func (t *Token) IsActive(now time.Time) bool {
	return t.RevokedAt == nil && now.Before(t.ExpiresAt)
}

// NormalizeTokenName recorta espacios y valida el largo (opcional).
func NormalizeTokenName(raw string) (string, error) {
	return optionalText("name", raw, TokenNameMaxLen)
}

// NormalizeTokenScopes valida los scopes (al menos uno, todos definidos) y
// los devuelve sin repetidos, en el orden del enum.
func NormalizeTokenScopes(scopes []TokenScope) ([]TokenScope, error) {
	if len(scopes) == 0 {
		return nil, domainerrors.ValidationError("at least one scope is required", map[string]any{"field": "scopes"})
	}
	for _, s := range scopes {
		if !s.IsValid() {
			return nil, domainerrors.ValidationError("invalid scope", map[string]any{"field": "scopes", "value": string(s)})
		}
	}
	out := make([]TokenScope, 0, len(tokenScopeOrder))
	for _, s := range tokenScopeOrder {
		if slices.Contains(scopes, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// TokenExpiry calcula el vencimiento de un token nuevo: requested nil = now
// + TokenDefaultTTL; si no, tiene que estar después de now y a TokenMaxTTL
// como máximo.
func TokenExpiry(now time.Time, requested *time.Time) (time.Time, error) {
	if requested == nil {
		return now.Add(TokenDefaultTTL), nil
	}
	if !requested.After(now) {
		return time.Time{}, domainerrors.ValidationError("expires_at must be in the future", map[string]any{"field": "expires_at"})
	}
	if requested.Sub(now) > TokenMaxTTL {
		return time.Time{}, domainerrors.ValidationError("expires_at is too far in the future", map[string]any{"field": "expires_at", "max_days": int(TokenMaxTTL / (24 * time.Hour))})
	}
	return *requested, nil
}

// LooksLikeToken informa si s tiene la forma de un token de Tekitla (prefijo
// y largo), sin tocar la base. Un token con otra forma no se busca.
func LooksLikeToken(s string) bool {
	// base64url sin padding de 32 bytes = 43 caracteres.
	return strings.HasPrefix(s, TokenPrefix) && len(s) == len(TokenPrefix)+43
}

// ResolvedToken es la identidad de un token válido: lo que propaga el
// gateway.
type ResolvedToken struct {
	TokenID string
	// UserUID es el uid de la persona responsable (dueña del token o de su
	// agente).
	UserUID       string
	PersonActorID string
	// AgentID: vacío si el token es de la persona.
	AgentID   string
	Scopes    []TokenScope
	ExpiresAt time.Time
}

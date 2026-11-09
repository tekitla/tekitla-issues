package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// ActorService implementa los casos de uso de actores, agentes y tokens
// (issue 025, ADR 0009).
//
// Lo usa solo una persona con sesión de Firebase, para sí misma o para sus
// agentes: un request de un agente (x-agent-id) lo rechaza el handler. Un
// actor o token ajeno responde NotFound, igual que uno inexistente, salvo
// CreateAgent con otro dueño (PermissionDenied).
type ActorService struct {
	actors actorAccess
	tokens port.TokenRepository
	now    func() time.Time
}

// NewActorService construye el servicio.
func NewActorService(actors port.ActorRepository, tokens port.TokenRepository) *ActorService {
	return &ActorService{actors: actorAccess{repo: actors}, tokens: tokens, now: utcNow}
}

// WithClock reemplaza el reloj (tests).
func (s *ActorService) WithClock(now func() time.Time) *ActorService {
	s.now = now
	return s
}

func utcNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// CreateAgentInput son los datos que acepta CreateAgent. OwnerActorID vacío
// = la persona del usuario.
type CreateAgentInput struct {
	DisplayName  string
	Tool         model.AgentTool
	OwnerActorID string
}

// CreateTokenInput son los datos que acepta CreateToken. ActorID vacío = la
// persona del usuario; ExpiresAt nil = 30 días.
type CreateTokenInput struct {
	ActorID   string
	Name      string
	Scopes    []model.TokenScope
	ExpiresAt *time.Time
}

// ListActors devuelve la persona del usuario (primero) y sus agentes por
// nombre. Sin includeRevoked, quita los revocados (también a la persona).
func (s *ActorService) ListActors(ctx context.Context, uid string, includeRevoked bool) ([]*model.Actor, error) {
	person, err := s.actors.person(ctx, uid, s.now())
	if err != nil {
		return nil, err
	}
	agents, err := s.actors.repo.ListAgents(ctx, person.ID, includeRevoked)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Actor, 0, len(agents)+1)
	if includeRevoked || !person.IsRevoked() {
		out = append(out, person)
	}
	return append(out, agents...), nil
}

// CreateAgent da de alta un agente de la persona del usuario.
func (s *ActorService) CreateAgent(ctx context.Context, uid string, in CreateAgentInput) (*model.Actor, error) {
	now := s.now()
	person, err := s.actors.person(ctx, uid, now)
	if err != nil {
		return nil, err
	}
	if in.OwnerActorID != "" && in.OwnerActorID != person.ID {
		return nil, domainerrors.PermissionDeniedError("an agent can only be created for yourself", map[string]any{"field": "owner_actor_id"})
	}
	if person.IsRevoked() {
		return nil, actorRevoked(person.ID)
	}
	name, err := model.NormalizeAgentName(in.DisplayName)
	if err != nil {
		return nil, err
	}
	if !in.Tool.IsValid() {
		return nil, domainerrors.ValidationError("invalid tool", map[string]any{"field": "tool", "value": string(in.Tool)})
	}
	return s.actors.repo.CreateAgent(ctx, &model.Actor{
		ID:           uuid.NewString(),
		Kind:         model.ActorKindAgent,
		OwnerActorID: person.ID,
		Tool:         in.Tool,
		DisplayName:  name,
		CreatedAt:    now,
	})
}

// RevokeActor revoca a la persona del usuario (con sus agentes) o a uno de
// sus agentes. No se deshace; uno ya revocado se devuelve tal cual.
func (s *ActorService) RevokeActor(ctx context.Context, uid, actorID string) (*model.Actor, error) {
	if err := validateUUID("actor_id", actorID); err != nil {
		return nil, err
	}
	now := s.now()
	person, err := s.actors.person(ctx, uid, now)
	if err != nil {
		return nil, err
	}
	target, err := s.actors.owned(ctx, person, actorID)
	if err != nil {
		return nil, err
	}
	if target.IsRevoked() {
		return target, nil
	}
	return s.actors.repo.Revoke(ctx, target.ID, now)
}

// CreateToken emite un token para la persona del usuario o uno de sus
// agentes. Devuelve el token y el secreto en claro, que no se guarda.
func (s *ActorService) CreateToken(ctx context.Context, uid string, in CreateTokenInput) (*model.Token, string, error) {
	now := s.now()
	person, err := s.actors.person(ctx, uid, now)
	if err != nil {
		return nil, "", err
	}
	actor := person
	if in.ActorID != "" {
		if err := validateUUID("actor_id", in.ActorID); err != nil {
			return nil, "", err
		}
		if actor, err = s.actors.owned(ctx, person, in.ActorID); err != nil {
			return nil, "", err
		}
	}
	// Un agente de una persona revocada está revocado también (Revoke lo
	// hace en cascada); se mira la persona por las dudas.
	if actor.IsRevoked() || person.IsRevoked() {
		return nil, "", actorRevoked(actor.ID)
	}
	name, err := model.NormalizeTokenName(in.Name)
	if err != nil {
		return nil, "", err
	}
	scopes, err := model.NormalizeTokenScopes(in.Scopes)
	if err != nil {
		return nil, "", err
	}
	expires, err := model.TokenExpiry(now, in.ExpiresAt)
	if err != nil {
		return nil, "", err
	}
	secret, err := newTokenSecret()
	if err != nil {
		return nil, "", err
	}
	token, err := s.tokens.Create(ctx, &model.Token{
		ID:        uuid.NewString(),
		ActorID:   actor.ID,
		Name:      name,
		Scopes:    scopes,
		CreatedAt: now,
		ExpiresAt: expires,
	}, HashToken(secret))
	if err != nil {
		return nil, "", err
	}
	return token, secret, nil
}

// ListTokens devuelve los tokens de la persona del usuario y de sus agentes
// (o solo los de actorID), del más nuevo al más viejo.
func (s *ActorService) ListTokens(ctx context.Context, uid, actorID string, includeInactive bool) ([]*model.Token, error) {
	now := s.now()
	ids, err := s.ownActorIDs(ctx, uid, now)
	if err != nil {
		return nil, err
	}
	if actorID != "" {
		if !contains(ids, actorID) {
			return nil, actorNotFound(actorID)
		}
		ids = []string{actorID}
	}
	return s.tokens.List(ctx, ids, includeInactive, now)
}

// RevokeToken revoca un token de la persona del usuario o de sus agentes.
// No se deshace; uno ya revocado se devuelve tal cual.
func (s *ActorService) RevokeToken(ctx context.Context, uid, tokenID string) (*model.Token, error) {
	if err := validateUUID("token_id", tokenID); err != nil {
		return nil, err
	}
	now := s.now()
	ids, err := s.ownActorIDs(ctx, uid, now)
	if err != nil {
		return nil, err
	}
	token, err := s.tokens.Get(ctx, tokenID)
	if domainerrors.IsNotFound(err) || (err == nil && !contains(ids, token.ActorID)) {
		return nil, domainerrors.NotFoundError("token not found", map[string]any{"id": tokenID})
	}
	if err != nil {
		return nil, err
	}
	if token.RevokedAt != nil {
		return token, nil
	}
	return s.tokens.Revoke(ctx, tokenID, now)
}

// ResolveToken resuelve un token en claro a la identidad que propaga el
// gateway. Unauthorized si no existe, venció o está revocado (él, su actor o
// el dueño del agente), sin decir cuál.
func (s *ActorService) ResolveToken(ctx context.Context, secret string) (*model.ResolvedToken, error) {
	invalid := domainerrors.UnauthorizedError("invalid token", nil)
	if !model.LooksLikeToken(secret) {
		return nil, invalid
	}
	resolved, err := s.tokens.Resolve(ctx, HashToken(secret), s.now())
	if domainerrors.IsNotFound(err) {
		return nil, invalid
	}
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

// ownActorIDs devuelve el id de la persona del usuario y los de todos sus
// agentes (también los revocados).
func (s *ActorService) ownActorIDs(ctx context.Context, uid string, now time.Time) ([]string, error) {
	person, err := s.actors.person(ctx, uid, now)
	if err != nil {
		return nil, err
	}
	agents, err := s.actors.repo.ListAgents(ctx, person.ID, true)
	if err != nil {
		return nil, err
	}
	ids := []string{person.ID}
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	return ids, nil
}

// HashToken es el SHA-256 del token en claro: lo único que se guarda.
func HashToken(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// newTokenSecret genera "tka_" + 32 bytes aleatorios en base64url.
func newTokenSecret() (string, error) {
	b := make([]byte, model.TokenSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", domainerrors.InternalError("failed to generate token", map[string]any{"cause": err.Error()})
	}
	return model.TokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// actorAccess resuelve la persona de un usuario y los actores que puede ver
// (ella y sus agentes). Lo comparten ActorService e IssueService (asignado).
type actorAccess struct {
	repo port.ActorRepository
}

// person devuelve la persona del usuario y la crea si no existe (ADR 0009:
// se crea la primera vez que hace falta).
func (a actorAccess) person(ctx context.Context, uid string, now time.Time) (*model.Actor, error) {
	if err := requireOwner(uid); err != nil {
		return nil, err
	}
	return a.repo.EnsurePerson(ctx, &model.Actor{ID: uuid.NewString(), Kind: model.ActorKindPerson, UserUID: uid, CreatedAt: now})
}

// owned devuelve actorID si es person o uno de sus agentes; si no, NotFound.
func (a actorAccess) owned(ctx context.Context, person *model.Actor, actorID string) (*model.Actor, error) {
	if actorID == person.ID {
		return person, nil
	}
	actor, err := a.repo.Get(ctx, actorID)
	if domainerrors.IsNotFound(err) || (err == nil && actor.OwnerActorID != person.ID) {
		return nil, actorNotFound(actorID)
	}
	return actor, err
}

// assignable valida un asignado: un actor que el usuario puede ver (él o uno
// de sus agentes) y no revocado. Cualquier otro caso es ValidationError
// sobre field (un actor ajeno no se distingue de uno inexistente).
func (a actorAccess) assignable(ctx context.Context, uid, field, actorID string, now time.Time) error {
	invalid := domainerrors.ValidationError("assignee is not an active actor you can see", map[string]any{"field": field, "value": actorID})
	if _, err := uuid.Parse(actorID); err != nil {
		return invalid
	}
	person, err := a.person(ctx, uid, now)
	if err != nil {
		return err
	}
	actor, err := a.owned(ctx, person, actorID)
	if domainerrors.IsNotFound(err) {
		return invalid
	}
	if err != nil {
		return err
	}
	if actor.IsRevoked() || person.IsRevoked() {
		return invalid
	}
	return nil
}

func actorNotFound(id string) error {
	return domainerrors.NotFoundError("actor not found", map[string]any{"id": id})
}

func actorRevoked(id string) error {
	return domainerrors.FailedPreconditionError("actor is revoked", map[string]any{"actor_id": id}).WithReason("ACTOR_REVOKED")
}

func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

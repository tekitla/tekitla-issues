package port

import (
	"context"
	"time"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// ActorRepository define la persistencia de actores (personas y agentes,
// issue 025).
//
// Contrato de errores (todas las implementaciones):
//   - no existe        -> domainerrors.NotFoundError
//   - nombre de agente repetido entre los no revocados del dueño ->
//     domainerrors.ConflictError (reason AGENT_NAME_TAKEN)
//   - cualquier otra cosa -> domainerrors.InternalError
type ActorRepository interface {
	// EnsurePerson devuelve la persona de person.UserUID y la crea (con
	// person.ID y CreatedAt) si no existe. Seguro con concurrencia. Puede
	// devolver una persona revocada.
	EnsurePerson(ctx context.Context, person *model.Actor) (*model.Actor, error)
	// Get devuelve un actor por id.
	Get(ctx context.Context, id string) (*model.Actor, error)
	// ListAgents devuelve los agentes de la persona ownerID por nombre (sin
	// mayúsculas); con includeRevoked, también los revocados.
	ListAgents(ctx context.Context, ownerID string, includeRevoked bool) ([]*model.Actor, error)
	// CreateAgent inserta el agente (todo seteado y ya validado).
	CreateAgent(ctx context.Context, agent *model.Actor) (*model.Actor, error)
	// Revoke revoca el actor en at; si es una persona, también a sus agentes
	// no revocados, en la misma transacción. Uno ya revocado no cambia.
	// Devuelve el actor como queda.
	Revoke(ctx context.Context, id string, at time.Time) (*model.Actor, error)
}

// TokenRepository define la persistencia de tokens (solo su SHA-256).
//
// Contrato de errores: no existe -> NotFoundError; hash repetido ->
// ConflictError; resto -> InternalError.
type TokenRepository interface {
	// Create guarda el token (todo seteado y ya validado) con su hash.
	Create(ctx context.Context, token *model.Token, hash []byte) (*model.Token, error)
	// Get devuelve un token por id.
	Get(ctx context.Context, id string) (*model.Token, error)
	// List devuelve los tokens de los actores actorIDs, del más nuevo al más
	// viejo. Sin includeInactive, solo los activos en now: sin revocar, sin
	// vencer y con su actor (y el dueño del agente) sin revocar.
	List(ctx context.Context, actorIDs []string, includeInactive bool, now time.Time) ([]*model.Token, error)
	// Revoke revoca el token en at. Uno ya revocado no cambia. Devuelve el
	// token como queda.
	Revoke(ctx context.Context, id string, at time.Time) (*model.Token, error)
	// Resolve busca el token activo con ese hash (sin revocar, sin vencer en
	// now, con su actor y el dueño del agente sin revocar), actualiza su
	// last_used_at a now y devuelve su identidad. NotFound en cualquier otro
	// caso, sin decir cuál.
	Resolve(ctx context.Context, hash []byte, now time.Time) (*model.ResolvedToken, error)
}

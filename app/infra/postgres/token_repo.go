package postgres

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// tokenRow es una fila de issues.tokens leída sin el hash (el hash no sale
// del repo). Scopes viene como texto separado por comas
// (array_to_string): los scopes no tienen comas.
type tokenRow struct {
	ID         string
	ActorID    string
	Name       string
	Scopes     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// tokenColumns son las columnas de tokenRow (nunca token_hash).
const tokenColumns = `t.id, t.actor_id, t.name, array_to_string(t.scopes, ',') AS scopes,
	t.created_at, t.expires_at, t.last_used_at, t.revoked_at`

// tokenActive es la condición de un token válido en ? (now): sin revocar,
// sin vencer y con su actor y el dueño del agente sin revocar. Usa los alias
// t (token), a (actor) y o (dueño, LEFT JOIN).
const tokenActive = `t.revoked_at IS NULL AND t.expires_at > ? AND a.revoked_at IS NULL AND o.revoked_at IS NULL`

const tokenJoins = `FROM issues.tokens t
	JOIN issues.actors a ON a.id = t.actor_id
	LEFT JOIN issues.actors o ON o.id = a.owner_actor_id`

// TokenRepository implementa port.TokenRepository sobre Postgres.
type TokenRepository struct{ db *gorm.DB }

var _ port.TokenRepository = (*TokenRepository)(nil)

// NewTokenRepository construye el repo.
func NewTokenRepository(db *gorm.DB) *TokenRepository {
	return &TokenRepository{db: db}
}

// Create guarda el token con su hash.
func (r *TokenRepository) Create(ctx context.Context, token *model.Token, hash []byte) (*model.Token, error) {
	err := r.db.WithContext(ctx).Exec(
		`INSERT INTO issues.tokens (id, actor_id, name, token_hash, scopes, created_at, expires_at)
		 VALUES (?, ?, ?, ?, string_to_array(?, ','), ?, ?)`,
		token.ID, token.ActorID, token.Name, hash, joinScopes(token.Scopes), token.CreatedAt, token.ExpiresAt,
	).Error
	if err != nil {
		return nil, translateError(err, "token", "create", map[string]any{"actor_id": token.ActorID})
	}
	out := *token
	return &out, nil
}

// Get devuelve un token por id.
func (r *TokenRepository) Get(ctx context.Context, id string) (*model.Token, error) {
	return getToken(r.db.WithContext(ctx), id)
}

func getToken(db *gorm.DB, id string) (*model.Token, error) {
	var rows []tokenRow
	if err := db.Raw(`SELECT `+tokenColumns+` FROM issues.tokens t WHERE t.id = ?`, id).Scan(&rows).Error; err != nil {
		return nil, translateError(err, "token", "get", map[string]any{"id": id})
	}
	if len(rows) == 0 {
		return nil, domainerrors.NotFoundError("token not found", map[string]any{"id": id})
	}
	return fromTokenRow(&rows[0]), nil
}

// List devuelve los tokens de actorIDs, del más nuevo al más viejo.
func (r *TokenRepository) List(ctx context.Context, actorIDs []string, includeInactive bool, now time.Time) ([]*model.Token, error) {
	if len(actorIDs) == 0 {
		return []*model.Token{}, nil
	}
	query := `SELECT ` + tokenColumns + ` ` + tokenJoins + ` WHERE t.actor_id IN ?`
	args := []any{actorIDs}
	if !includeInactive {
		query += ` AND ` + tokenActive
		args = append(args, now)
	}
	query += ` ORDER BY t.created_at DESC, t.id DESC`
	var rows []tokenRow
	if err := r.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, translateError(err, "tokens", "list", nil)
	}
	out := make([]*model.Token, len(rows))
	for i := range rows {
		out[i] = fromTokenRow(&rows[i])
	}
	return out, nil
}

// Revoke revoca el token (uno ya revocado no cambia).
func (r *TokenRepository) Revoke(ctx context.Context, id string, at time.Time) (*model.Token, error) {
	db := r.db.WithContext(ctx)
	err := db.Exec(`UPDATE issues.tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, at, id).Error
	if err != nil {
		return nil, translateError(err, "token", "revoke", map[string]any{"id": id})
	}
	return getToken(db, id)
}

// resolvedRow es el resultado de Resolve.
type resolvedRow struct {
	ID           string
	Scopes       string
	ExpiresAt    time.Time
	ActorID      string
	ActorKind    string
	ActorUserUID *string
	OwnerID      *string
	OwnerUserUID *string
	OwnerKind    *string
}

// Resolve busca el token activo con ese hash y marca su uso, en una sola
// sentencia (UPDATE ... FROM ... RETURNING).
func (r *TokenRepository) Resolve(ctx context.Context, hash []byte, now time.Time) (*model.ResolvedToken, error) {
	var rows []resolvedRow
	err := r.db.WithContext(ctx).Raw(
		`UPDATE issues.tokens t
		    SET last_used_at = ?
		   FROM issues.actors a
		   LEFT JOIN issues.actors o ON o.id = a.owner_actor_id
		  WHERE t.token_hash = ? AND a.id = t.actor_id AND `+tokenActive+`
		RETURNING t.id, array_to_string(t.scopes, ',') AS scopes, t.expires_at,
		          a.id AS actor_id, a.kind AS actor_kind, a.user_uid AS actor_user_uid,
		          o.id AS owner_id, o.user_uid AS owner_user_uid, o.kind AS owner_kind`,
		now, hash, now,
	).Scan(&rows).Error
	if err != nil {
		return nil, translateError(err, "token", "resolve", nil)
	}
	if len(rows) == 0 {
		return nil, domainerrors.NotFoundError("token not found", nil)
	}
	row := rows[0]
	out := &model.ResolvedToken{TokenID: row.ID, Scopes: splitScopes(row.Scopes), ExpiresAt: row.ExpiresAt}
	switch {
	case row.ActorKind == string(model.ActorKindPerson) && row.ActorUserUID != nil:
		out.UserUID, out.PersonActorID = *row.ActorUserUID, row.ActorID
	case row.ActorKind == string(model.ActorKindAgent) && row.OwnerID != nil && row.OwnerUserUID != nil &&
		row.OwnerKind != nil && *row.OwnerKind == string(model.ActorKindPerson):
		out.UserUID, out.PersonActorID, out.AgentID = *row.OwnerUserUID, *row.OwnerID, row.ActorID
	default:
		// Un agente cuyo dueño no es una persona: problema de datos. Mejor no
		// autenticar que propagar una identidad rara.
		return nil, domainerrors.InternalError("token actor is inconsistent", map[string]any{"token_id": row.ID})
	}
	return out, nil
}

func joinScopes(scopes []model.TokenScope) string {
	parts := make([]string, len(scopes))
	for i, s := range scopes {
		parts[i] = string(s)
	}
	return strings.Join(parts, ",")
}

func splitScopes(s string) []model.TokenScope {
	if s == "" {
		return []model.TokenScope{}
	}
	parts := strings.Split(s, ",")
	out := make([]model.TokenScope, len(parts))
	for i, p := range parts {
		out[i] = model.TokenScope(p)
	}
	return out
}

func fromTokenRow(r *tokenRow) *model.Token {
	return &model.Token{
		ID:         r.ID,
		ActorID:    r.ActorID,
		Name:       r.Name,
		Scopes:     splitScopes(r.Scopes),
		CreatedAt:  r.CreatedAt,
		ExpiresAt:  r.ExpiresAt,
		LastUsedAt: r.LastUsedAt,
		RevokedAt:  r.RevokedAt,
	}
}

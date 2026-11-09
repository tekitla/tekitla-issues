package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// actorRow es la fila de issues.actors (issue 025). UserUID solo en
// personas; OwnerActorID y Tool solo en agentes (CHECKs de 000005).
type actorRow struct {
	ID           string     `gorm:"primaryKey;column:id;type:uuid"`
	Kind         string     `gorm:"column:kind;not null"`
	UserUID      *string    `gorm:"column:user_uid"`
	OwnerActorID *string    `gorm:"column:owner_actor_id;type:uuid"`
	Tool         *string    `gorm:"column:tool"`
	DisplayName  string     `gorm:"column:display_name;not null"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	RevokedAt    *time.Time `gorm:"column:revoked_at"`
}

func (actorRow) TableName() string { return "issues.actors" }

// Índice único del nombre de agente por dueño (ver la migración 000005).
const agentNameIndex = "actors_owner_lower_name_key"

// ActorRepository implementa port.ActorRepository sobre Postgres.
type ActorRepository struct{ db *gorm.DB }

var _ port.ActorRepository = (*ActorRepository)(nil)

// NewActorRepository construye el repo.
func NewActorRepository(db *gorm.DB) *ActorRepository {
	return &ActorRepository{db: db}
}

// EnsurePerson inserta la persona con ON CONFLICT DO NOTHING sobre el uid
// único y después la lee: dos llamadas concurrentes terminan con la misma.
func (r *ActorRepository) EnsurePerson(ctx context.Context, person *model.Actor) (*model.Actor, error) {
	db := r.db.WithContext(ctx)
	details := map[string]any{"user_uid": person.UserUID}
	err := db.Exec(
		`INSERT INTO issues.actors (id, kind, user_uid, display_name, created_at)
		 VALUES (?, 'person', ?, '', ?)
		 ON CONFLICT (user_uid) DO NOTHING`,
		person.ID, person.UserUID, person.CreatedAt,
	).Error
	if err != nil {
		return nil, translateError(err, "actor", "create", details)
	}
	var row actorRow
	if err := db.Where("user_uid = ?", person.UserUID).First(&row).Error; err != nil {
		return nil, translateError(err, "actor", "get", details)
	}
	return fromActorRow(&row), nil
}

// Get devuelve un actor por id.
func (r *ActorRepository) Get(ctx context.Context, id string) (*model.Actor, error) {
	var row actorRow
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, translateError(err, "actor", "get", map[string]any{"id": id})
	}
	return fromActorRow(&row), nil
}

// ListAgents devuelve los agentes de ownerID por nombre sin mayúsculas.
func (r *ActorRepository) ListAgents(ctx context.Context, ownerID string, includeRevoked bool) ([]*model.Actor, error) {
	query := r.db.WithContext(ctx).Where("owner_actor_id = ?", ownerID)
	if !includeRevoked {
		query = query.Where("revoked_at IS NULL")
	}
	var rows []actorRow
	if err := query.Order("lower(display_name) ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, translateError(err, "actors", "list", map[string]any{"owner_actor_id": ownerID})
	}
	out := make([]*model.Actor, len(rows))
	for i := range rows {
		out[i] = fromActorRow(&rows[i])
	}
	return out, nil
}

// CreateAgent inserta el agente solo si su dueño es una persona sin
// revocar, en la misma sentencia y con FOR SHARE sobre el dueño: un Revoke
// concurrente (FOR UPDATE) espera, o lo hace fallar si ganó (entonces
// FailedPrecondition ACTOR_REVOKED). Así no queda un agente sin revocar de
// una persona revocada.
func (r *ActorRepository) CreateAgent(ctx context.Context, agent *model.Actor) (*model.Actor, error) {
	row := toActorRow(agent)
	res := r.db.WithContext(ctx).Exec(
		`INSERT INTO issues.actors (id, kind, owner_actor_id, tool, display_name, created_at)
		 SELECT ?, 'agent', ?, ?, ?, ?
		  WHERE EXISTS (SELECT 1 FROM issues.actors o
		                 WHERE o.id = ? AND o.kind = 'person' AND o.revoked_at IS NULL
		                   FOR SHARE)`,
		row.ID, row.OwnerActorID, row.Tool, row.DisplayName, row.CreatedAt, row.OwnerActorID,
	)
	if err := res.Error; err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == agentNameIndex {
			return nil, domainerrors.ConflictError("agent name already exists", map[string]any{"display_name": agent.DisplayName}).
				WithReason("AGENT_NAME_TAKEN")
		}
		return nil, translateError(err, "agent", "create", map[string]any{"display_name": agent.DisplayName})
	}
	if res.RowsAffected == 0 {
		return nil, domainerrors.FailedPreconditionError("owner is revoked", map[string]any{"actor_id": agent.OwnerActorID}).
			WithReason("ACTOR_REVOKED")
	}
	return fromActorRow(row), nil
}

// Revoke revoca el actor y, si es una persona, a sus agentes no revocados,
// en una transacción, con la fila del actor tomada con FOR UPDATE (ver
// CreateAgent). Además Resolve mira también al dueño, y CreateToken rechaza a
// una persona revocada.
func (r *ActorRepository) Revoke(ctx context.Context, id string, at time.Time) (*model.Actor, error) {
	details := map[string]any{"id": id}
	var out *model.Actor
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []actorRow
		if err := tx.Raw(`SELECT * FROM issues.actors WHERE id = ? FOR UPDATE`, id).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return gorm.ErrRecordNotFound
		}
		row := rows[0]
		if row.RevokedAt != nil {
			out = fromActorRow(&row)
			return nil
		}
		if err := tx.Exec(`UPDATE issues.actors SET revoked_at = ? WHERE id = ?`, at, id).Error; err != nil {
			return err
		}
		if row.Kind == string(model.ActorKindPerson) {
			if err := tx.Exec(`UPDATE issues.actors SET revoked_at = ? WHERE owner_actor_id = ? AND revoked_at IS NULL`, at, id).Error; err != nil {
				return err
			}
		}
		row.RevokedAt = &at
		out = fromActorRow(&row)
		return nil
	})
	if err != nil {
		return nil, translateError(err, "actor", "revoke", details)
	}
	return out, nil
}

func toActorRow(m *model.Actor) *actorRow {
	row := &actorRow{
		ID:          m.ID,
		Kind:        string(m.Kind),
		DisplayName: m.DisplayName,
		CreatedAt:   m.CreatedAt,
		RevokedAt:   m.RevokedAt,
	}
	if m.UserUID != "" {
		row.UserUID = &m.UserUID
	}
	if m.OwnerActorID != "" {
		row.OwnerActorID = &m.OwnerActorID
	}
	if m.Tool != model.AgentToolUnspecified {
		tool := string(m.Tool)
		row.Tool = &tool
	}
	return row
}

func fromActorRow(r *actorRow) *model.Actor {
	a := &model.Actor{
		ID:          r.ID,
		Kind:        model.ActorKind(r.Kind),
		DisplayName: r.DisplayName,
		CreatedAt:   r.CreatedAt,
		RevokedAt:   r.RevokedAt,
	}
	if r.UserUID != nil {
		a.UserUID = *r.UserUID
	}
	if r.OwnerActorID != nil {
		a.OwnerActorID = *r.OwnerActorID
	}
	if r.Tool != nil {
		a.Tool = model.AgentTool(*r.Tool)
	}
	return a
}

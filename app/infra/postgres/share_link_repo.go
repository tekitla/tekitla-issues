package postgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// shareLinkRow es una fila de issues.share_links leída sin el hash (el hash
// no sale del repo).
type shareLinkRow struct {
	ID               string
	ProjectID        string
	Scope            string
	Note             string
	OwnerDisplayName string
	CreatedByUID     string
	CreatedAt        time.Time
	ExpiresAt        *time.Time
	LastUsedAt       *time.Time
	RevokedAt        *time.Time
}

// shareLinkColumns son las columnas de shareLinkRow (nunca token_hash), con
// el alias l.
const shareLinkColumns = `l.id, l.project_id, l.scope, l.note, l.owner_display_name, l.created_by_uid,
	l.created_at, l.expires_at, l.last_used_at, l.revoked_at`

// shareLinkActive es la condición de un enlace válido en ? (now).
const shareLinkActive = `l.revoked_at IS NULL AND (l.expires_at IS NULL OR l.expires_at > ?)`

// ShareLinkRepository implementa port.ShareLinkRepository sobre Postgres.
type ShareLinkRepository struct{ db *gorm.DB }

var _ port.ShareLinkRepository = (*ShareLinkRepository)(nil)

// NewShareLinkRepository construye el repo.
func NewShareLinkRepository(db *gorm.DB) *ShareLinkRepository {
	return &ShareLinkRepository{db: db}
}

// Create guarda el enlace con su hash, con el lock del proyecto tomado para
// que dos altas a la vez no se pasen del tope.
func (r *ShareLinkRepository) Create(ctx context.Context, link *model.ShareLink, hash []byte, maxActive int) (*model.ShareLink, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProject(tx, link.ProjectID); err != nil {
			return err
		}
		var active int64
		err := tx.Raw(`SELECT count(*) FROM issues.share_links l WHERE l.project_id = ? AND `+shareLinkActive,
			link.ProjectID, link.CreatedAt).Scan(&active).Error
		if err != nil {
			return err
		}
		if active >= int64(maxActive) {
			return domainerrors.FailedPreconditionError("project has the maximum number of active share links",
				map[string]any{"max": maxActive}).WithReason("SHARE_LINK_LIMIT")
		}
		return tx.Exec(
			`INSERT INTO issues.share_links (id, project_id, token_hash, scope, note, owner_display_name,
			                                 created_by_uid, created_at, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			link.ID, link.ProjectID, hash, string(link.Scope), link.Note, link.OwnerDisplayName,
			link.CreatedByUID, link.CreatedAt, link.ExpiresAt,
		).Error
	})
	if err != nil {
		// El tope y el proyecto que no existe (lockProject) ya son de dominio.
		var de *domainerrors.DomainError
		if errors.As(err, &de) {
			return nil, de
		}
		return nil, translateError(err, "share link", "create", map[string]any{"project_id": link.ProjectID})
	}
	out := *link
	return &out, nil
}

// List devuelve los enlaces del proyecto, los más nuevos primero.
func (r *ShareLinkRepository) List(ctx context.Context, projectID string) ([]*model.ShareLink, error) {
	var rows []shareLinkRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT `+shareLinkColumns+` FROM issues.share_links l
		  WHERE l.project_id = ? ORDER BY l.created_at DESC, l.id DESC`, projectID).Scan(&rows).Error
	if err != nil {
		return nil, translateError(err, "share links", "list", map[string]any{"project_id": projectID})
	}
	out := make([]*model.ShareLink, len(rows))
	for i := range rows {
		out[i] = fromShareLinkRow(&rows[i])
	}
	return out, nil
}

// Get devuelve un enlace del proyecto.
func (r *ShareLinkRepository) Get(ctx context.Context, projectID, id string) (*model.ShareLink, error) {
	return getShareLink(r.db.WithContext(ctx), projectID, id)
}

func getShareLink(db *gorm.DB, projectID, id string) (*model.ShareLink, error) {
	var rows []shareLinkRow
	err := db.Raw(`SELECT `+shareLinkColumns+` FROM issues.share_links l WHERE l.project_id = ? AND l.id = ?`,
		projectID, id).Scan(&rows).Error
	if err != nil {
		return nil, translateError(err, "share link", "get", map[string]any{"id": id})
	}
	if len(rows) == 0 {
		return nil, domainerrors.NotFoundError("share link not found", map[string]any{"id": id})
	}
	return fromShareLinkRow(&rows[0]), nil
}

// Revoke revoca el enlace (uno ya revocado no cambia).
func (r *ShareLinkRepository) Revoke(ctx context.Context, projectID, id string, at time.Time) (*model.ShareLink, error) {
	db := r.db.WithContext(ctx)
	err := db.Exec(`UPDATE issues.share_links SET revoked_at = ? WHERE project_id = ? AND id = ? AND revoked_at IS NULL`,
		at, projectID, id).Error
	if err != nil {
		return nil, translateError(err, "share link", "revoke", map[string]any{"id": id})
	}
	return getShareLink(db, projectID, id)
}

// resolvedShareLinkRow es el resultado de Resolve: shareLinkRow más el
// espacio del proyecto. Plano a propósito: GORM no llena un struct embebido
// al escanear un Raw.
type resolvedShareLinkRow struct {
	ID               string
	ProjectID        string
	Scope            string
	Note             string
	OwnerDisplayName string
	CreatedByUID     string
	CreatedAt        time.Time
	ExpiresAt        *time.Time
	LastUsedAt       *time.Time
	RevokedAt        *time.Time
	WorkspaceID      string
}

// Resolve busca el enlace activo con ese hash (un SELECT por el índice único,
// sin locks) y, si su último uso tiene más de lastUsedEvery, lo marca. No hay
// caché: revocar corta el siguiente request.
func (r *ShareLinkRepository) Resolve(ctx context.Context, hash []byte, now time.Time) (*model.ResolvedShareLink, error) {
	db := r.db.WithContext(ctx)
	var rows []resolvedShareLinkRow
	err := db.Raw(
		`SELECT `+shareLinkColumns+`, p.workspace_id
		   FROM issues.share_links l
		   JOIN issues.projects p ON p.id = l.project_id
		  WHERE l.token_hash = ? AND `+shareLinkActive,
		hash, now,
	).Scan(&rows).Error
	if err != nil {
		return nil, translateError(err, "share link", "resolve", nil)
	}
	if len(rows) == 0 {
		return nil, domainerrors.NotFoundError("share link not found", nil)
	}
	row := rows[0]
	if row.LastUsedAt == nil || now.Sub(*row.LastUsedAt) >= lastUsedEvery {
		err := db.Exec(`UPDATE issues.share_links SET last_used_at = ?
		                 WHERE id = ? AND (last_used_at IS NULL OR last_used_at <= ?)`,
			now, row.ID, now.Add(-lastUsedEvery)).Error
		if err != nil {
			return nil, translateError(err, "share link", "resolve", nil)
		}
	}
	link := fromShareLinkRow(&shareLinkRow{
		ID: row.ID, ProjectID: row.ProjectID, Scope: row.Scope, Note: row.Note, OwnerDisplayName: row.OwnerDisplayName,
		CreatedByUID: row.CreatedByUID, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, LastUsedAt: row.LastUsedAt,
		RevokedAt: row.RevokedAt,
	})
	return &model.ResolvedShareLink{Link: link, WorkspaceID: row.WorkspaceID}, nil
}

func fromShareLinkRow(r *shareLinkRow) *model.ShareLink {
	return &model.ShareLink{
		ID:               r.ID,
		ProjectID:        r.ProjectID,
		Scope:            model.ShareScope(r.Scope),
		Note:             r.Note,
		OwnerDisplayName: r.OwnerDisplayName,
		CreatedByUID:     r.CreatedByUID,
		CreatedAt:        r.CreatedAt,
		ExpiresAt:        r.ExpiresAt,
		LastUsedAt:       r.LastUsedAt,
		RevokedAt:        r.RevokedAt,
	}
}

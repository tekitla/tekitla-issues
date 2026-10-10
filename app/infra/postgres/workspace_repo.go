package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// workspaceRow es la fila de issues.workspaces (espacios personales: uno por
// owner_uid).
type workspaceRow struct {
	ID        string    `gorm:"primaryKey;column:id;type:uuid"`
	OwnerUID  string    `gorm:"column:owner_uid;not null"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (workspaceRow) TableName() string { return "issues.workspaces" }

// WorkspaceRepository implementa port.WorkspaceRepository sobre Postgres.
type WorkspaceRepository struct{ db *gorm.DB }

var _ port.WorkspaceRepository = (*WorkspaceRepository)(nil)

// NewWorkspaceRepository construye el repo.
func NewWorkspaceRepository(db *gorm.DB) *WorkspaceRepository {
	return &WorkspaceRepository{db: db}
}

// GetPersonal busca el espacio de ownerUID; sin espacio -> NotFound.
func (r *WorkspaceRepository) GetPersonal(ctx context.Context, ownerUID string) (*model.Workspace, error) {
	var row workspaceRow
	if err := r.db.WithContext(ctx).Where("owner_uid = ?", ownerUID).First(&row).Error; err != nil {
		return nil, translateError(err, "workspace", "get", map[string]any{"owner_uid": ownerUID})
	}
	return fromWorkspaceRow(&row), nil
}

// EnsurePersonal lee el espacio de ws.OwnerUID y, solo si no existe, lo
// inserta con INSERT ... ON CONFLICT (owner_uid) DO NOTHING y lo vuelve a
// leer: si dos primeros accesos compiten, gana uno y ambos devuelven esa fila.
// Todo error sale como Internal (incluso no encontrarlo después del INSERT).
func (r *WorkspaceRepository) EnsurePersonal(ctx context.Context, ws *model.Workspace) (*model.Workspace, error) {
	existing, err := r.GetPersonal(ctx, ws.OwnerUID)
	if err == nil {
		return existing, nil
	}
	if !domainerrors.IsNotFound(err) {
		return nil, err
	}

	row := &workspaceRow{ID: ws.ID, OwnerUID: ws.OwnerUID, CreatedAt: dbNow()}
	err = r.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "owner_uid"}}, DoNothing: true}).
		Create(row).Error
	if err != nil {
		return nil, domainerrors.InternalError("failed to create workspace", map[string]any{"owner_uid": ws.OwnerUID, "cause": err.Error()})
	}
	stored, err := r.GetPersonal(ctx, ws.OwnerUID)
	if err != nil {
		return nil, domainerrors.InternalError("failed to get workspace after create", map[string]any{"owner_uid": ws.OwnerUID, "cause": err.Error()})
	}
	return stored, nil
}

func fromWorkspaceRow(r *workspaceRow) *model.Workspace {
	return &model.Workspace{ID: r.ID, OwnerUID: r.OwnerUID, CreatedAt: r.CreatedAt}
}

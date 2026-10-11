package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// labelRow es la fila de issues.labels. Color es 1..7 (CHECK en la migración).
type labelRow struct {
	ID        string    `gorm:"primaryKey;column:id;type:uuid"`
	ProjectID string    `gorm:"column:project_id;type:uuid;not null"`
	Name      string    `gorm:"column:name;not null"`
	Color     int       `gorm:"column:color;not null"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (labelRow) TableName() string { return "issues.labels" }

// Índice único de nombre sin mayúsculas (ver la migración 000003).
const labelNameIndex = "labels_project_id_lower_name_key"

// LabelRepository implementa port.LabelRepository sobre Postgres.
type LabelRepository struct{ db *gorm.DB }

var _ port.LabelRepository = (*LabelRepository)(nil)

// NewLabelRepository construye el repo.
func NewLabelRepository(db *gorm.DB) *LabelRepository {
	return &LabelRepository{db: db}
}

// List devuelve las etiquetas del proyecto por nombre (sin mayúsculas).
func (r *LabelRepository) List(ctx context.Context, projectID string) ([]*model.Label, error) {
	var rows []labelRow
	err := r.db.WithContext(ctx).Where("project_id = ?", projectID).Order("lower(name) ASC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, translateError(err, "labels", "list", map[string]any{"project_id": projectID})
	}
	out := make([]*model.Label, len(rows))
	for i := range rows {
		out[i] = fromLabelRow(&rows[i])
	}
	return out, nil
}

// Create inserta la etiqueta, con el lock del proyecto para respetar el
// máximo.
func (r *LabelRepository) Create(ctx context.Context, label *model.Label) (*model.Label, error) {
	row := toLabelRow(label)
	now := dbNow()
	row.CreatedAt, row.UpdatedAt = now, now
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProject(tx, label.ProjectID); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&labelRow{}).Where("project_id = ?", label.ProjectID).Count(&count).Error; err != nil {
			return err
		}
		if count >= model.MaxLabelsPerProject {
			return domainerrors.FailedPreconditionError("project has the maximum number of labels", map[string]any{"max": model.MaxLabelsPerProject})
		}
		return tx.Create(row).Error
	})
	if err != nil {
		return nil, labelError(err, "create", map[string]any{"name": label.Name})
	}
	return fromLabelRow(row), nil
}

// Update cambia nombre y/o color.
func (r *LabelRepository) Update(ctx context.Context, projectID, id string, name *string, color *model.LabelColor) (*model.Label, error) {
	updates := map[string]any{"updated_at": dbNow()}
	if name != nil {
		updates["name"] = *name
	}
	if color != nil {
		updates["color"] = int(*color)
	}
	var rows []labelRow
	res := r.db.WithContext(ctx).Model(&rows).Clauses(clause.Returning{}).
		Where("project_id = ? AND id = ?", projectID, id).Updates(updates)
	details := map[string]any{"id": id}
	if res.Error != nil {
		return nil, labelError(res.Error, "update", details)
	}
	if res.RowsAffected == 0 || len(rows) == 0 {
		return nil, domainerrors.NotFoundError("label not found", details)
	}
	return fromLabelRow(&rows[0]), nil
}

// Delete borra la etiqueta; issue_labels la quita de los issues (ON DELETE
// CASCADE).
func (r *LabelRepository) Delete(ctx context.Context, projectID, id string) error {
	res := r.db.WithContext(ctx).Where("project_id = ? AND id = ?", projectID, id).Delete(&labelRow{})
	details := map[string]any{"id": id}
	if res.Error != nil {
		return translateError(res.Error, "label", "delete", details)
	}
	if res.RowsAffected == 0 {
		return domainerrors.NotFoundError("label not found", details)
	}
	return nil
}

// labelError: el nombre repetido lleva su reason; los DomainError pasan tal
// cual.
func labelError(err error, op string, details map[string]any) error {
	var de *domainerrors.DomainError
	if errors.As(err, &de) {
		return de
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == labelNameIndex {
		return domainerrors.ConflictError("label name already exists", details).WithReason("LABEL_NAME_TAKEN")
	}
	return translateError(err, "label", op, details)
}

func toLabelRow(m *model.Label) *labelRow {
	return &labelRow{
		ID:        m.ID,
		ProjectID: m.ProjectID,
		Name:      m.Name,
		Color:     int(m.Color),
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func fromLabelRow(r *labelRow) *model.Label {
	return &model.Label{
		ID:        r.ID,
		ProjectID: r.ProjectID,
		Name:      r.Name,
		Color:     model.LabelColor(r.Color),
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

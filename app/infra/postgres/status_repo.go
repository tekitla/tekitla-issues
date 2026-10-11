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

// statusRow es la fila de issues.statuses. Category se guarda como texto
// (mismos valores que issues.state) y Rank como TEXT COLLATE "C".
type statusRow struct {
	ID        string    `gorm:"primaryKey;column:id;type:uuid"`
	ProjectID string    `gorm:"column:project_id;type:uuid;not null"`
	Name      string    `gorm:"column:name;not null"`
	Category  string    `gorm:"column:category;not null"`
	Rank      string    `gorm:"column:rank;not null"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (statusRow) TableName() string { return "issues.statuses" }

// Índice único de nombre sin mayúsculas (ver la migración 000003).
const statusNameIndex = "statuses_project_id_lower_name_key"

// StatusRepository implementa port.StatusRepository sobre Postgres.
type StatusRepository struct{ db *gorm.DB }

var _ port.StatusRepository = (*StatusRepository)(nil)

// NewStatusRepository construye el repo.
func NewStatusRepository(db *gorm.DB) *StatusRepository {
	return &StatusRepository{db: db}
}

// List devuelve los estados del proyecto por rank.
func (r *StatusRepository) List(ctx context.Context, projectID string) ([]*model.Status, error) {
	rows, err := listStatusRows(r.db.WithContext(ctx), projectID)
	if err != nil {
		return nil, translateError(err, "statuses", "list", map[string]any{"project_id": projectID})
	}
	out := make([]*model.Status, len(rows))
	for i := range rows {
		out[i] = fromStatusRow(&rows[i])
	}
	return out, nil
}

// Get devuelve un estado del proyecto.
func (r *StatusRepository) Get(ctx context.Context, projectID, id string) (*model.Status, error) {
	row, err := getStatusRow(r.db.WithContext(ctx), projectID, id)
	if err != nil {
		return nil, translateError(err, "status", "get", map[string]any{"id": id})
	}
	return fromStatusRow(row), nil
}

// Create inserta el estado después de afterID ("" = al final), con el lock
// del proyecto tomado.
func (r *StatusRepository) Create(ctx context.Context, status *model.Status, afterID string) (*model.Status, error) {
	row := toStatusRow(status)
	now := dbNow()
	row.CreatedAt, row.UpdatedAt = now, now
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProject(tx, status.ProjectID); err != nil {
			return err
		}
		rows, err := listStatusRows(tx, status.ProjectID)
		if err != nil {
			return err
		}
		if len(rows) >= model.MaxStatusesPerProject {
			return domainerrors.FailedPreconditionError("project has the maximum number of statuses", map[string]any{"max": model.MaxStatusesPerProject})
		}
		after, err := afterIndex(rows, afterID, "after_status_id", len(rows)-1)
		if err != nil {
			return err
		}
		row.Rank, err = rankAfterIndex(rows, after)
		if err != nil {
			return err
		}
		return tx.Create(row).Error
	})
	if err != nil {
		return nil, statusError(err, "create", map[string]any{"name": status.Name})
	}
	return fromStatusRow(row), nil
}

// Update renombra y/o reubica el estado.
func (r *StatusRepository) Update(ctx context.Context, projectID, id string, name, afterID *string) (*model.Status, error) {
	var out *statusRow
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProject(tx, projectID); err != nil {
			return err
		}
		row, err := getStatusRow(tx, projectID, id)
		if err != nil {
			return err
		}
		updates := map[string]any{"updated_at": dbNow()}
		if name != nil {
			updates["name"] = *name
		}
		if afterID != nil {
			rows, err := listStatusRows(tx, projectID)
			if err != nil {
				return err
			}
			// Se calcula el rank sin el propio estado en la lista.
			others := make([]statusRow, 0, len(rows))
			for _, s := range rows {
				if s.ID != id {
					others = append(others, s)
				}
			}
			after, err := afterIndex(others, *afterID, "after_status_id", -1)
			if err != nil {
				return err
			}
			rank, err := rankAfterIndex(others, after)
			if err != nil {
				return err
			}
			updates["rank"] = rank
		}
		var updated []statusRow
		if err := tx.Model(&updated).Clauses(clause.Returning{}).
			Where("project_id = ? AND id = ?", projectID, row.ID).Updates(updates).Error; err != nil {
			return err
		}
		out = &updated[0]
		return nil
	})
	if err != nil {
		return nil, statusError(err, "update", map[string]any{"id": id})
	}
	return fromStatusRow(out), nil
}

// Delete borra el estado y pasa sus issues a moveToID, con un evento por
// issue movido.
func (r *StatusRepository) Delete(ctx context.Context, projectID, id, moveToID, actorUID string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProject(tx, projectID); err != nil {
			return err
		}
		rows, err := listStatusRows(tx, projectID)
		if err != nil {
			return err
		}
		var status *statusRow
		sameCategory := 0
		for i := range rows {
			if rows[i].ID == id {
				status = &rows[i]
			}
		}
		if status == nil {
			return domainerrors.NotFoundError("status not found", map[string]any{"id": id})
		}
		for _, s := range rows {
			if s.Category == status.Category {
				sameCategory++
			}
		}
		if sameCategory == 1 {
			return domainerrors.FailedPreconditionError("cannot delete the last status of its category", map[string]any{"category": status.Category}).
				WithReason("LAST_STATUS_OF_CATEGORY")
		}

		var issues []issueRow
		if err := tx.Where("project_id = ? AND status_id = ?", projectID, id).Order("rank ASC").Find(&issues).Error; err != nil {
			return err
		}
		if len(issues) > 0 {
			if moveToID == "" {
				return domainerrors.FailedPreconditionError("status has issues: move_to_status_id is required", map[string]any{"issues": len(issues)}).
					WithReason("STATUS_HAS_ISSUES")
			}
			var target *statusRow
			for i := range rows {
				if rows[i].ID == moveToID {
					target = &rows[i]
				}
			}
			if target == nil {
				return domainerrors.ValidationError("move_to_status_id is not part of the project", map[string]any{"field": "move_to_status_id", "value": moveToID})
			}
			now := dbNow()
			if err := tx.Model(&issueRow{}).Where("project_id = ? AND status_id = ?", projectID, id).
				Updates(map[string]any{"status_id": target.ID, "state": target.Category, "updated_at": now}).Error; err != nil {
				return err
			}
			change := model.NewStatusChange(fromStatusRow(status), fromStatusRow(target))
			for _, it := range issues {
				ev := &model.IssueEvent{Kind: model.EventStatusChanged, Status: change}
				if err := insertEvent(tx, it.ID, actorUID, ev, now); err != nil {
					return err
				}
			}
		}
		return tx.Where("project_id = ? AND id = ?", projectID, id).Delete(&statusRow{}).Error
	})
	if err != nil {
		return statusError(err, "delete", map[string]any{"id": id})
	}
	return nil
}

// lockProject toma el lock de la fila del proyecto: serializa las escrituras
// que dependen del conjunto de estados o del orden del proyecto.
func lockProject(tx *gorm.DB, projectID string) error {
	var ids []string
	if err := tx.Raw(`SELECT id FROM issues.projects WHERE id = ? FOR UPDATE`, projectID).Scan(&ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return domainerrors.NotFoundError("project not found", map[string]any{"id": projectID})
	}
	return nil
}

func listStatusRows(db *gorm.DB, projectID string) ([]statusRow, error) {
	var rows []statusRow
	err := db.Where("project_id = ?", projectID).Order("rank ASC").Find(&rows).Error
	return rows, err
}

func getStatusRow(db *gorm.DB, projectID, id string) (*statusRow, error) {
	var row statusRow
	if err := db.Where("project_id = ? AND id = ?", projectID, id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// afterIndex devuelve la posición de afterID en rows; afterID vacío devuelve
// emptyIndex (len-1 = al final, -1 = al principio). Un id que no está ->
// ValidationError sobre field.
func afterIndex(rows []statusRow, afterID, field string, emptyIndex int) (int, error) {
	if afterID == "" {
		return emptyIndex, nil
	}
	for i, s := range rows {
		if s.ID == afterID {
			return i, nil
		}
	}
	return 0, domainerrors.ValidationError(field+" is not part of the project", map[string]any{"field": field, "value": afterID})
}

// rankAfterIndex devuelve un rank entre rows[after] y rows[after+1] (after =
// -1: antes del primero).
func rankAfterIndex(rows []statusRow, after int) (string, error) {
	lo, hi := "", ""
	if after >= 0 {
		lo = rows[after].Rank
	}
	if after+1 < len(rows) {
		hi = rows[after+1].Rank
	}
	return model.RankBetween(lo, hi)
}

// statusError traduce los errores de escritura de estados: el nombre
// repetido lleva su reason; los DomainError ya armados pasan tal cual.
func statusError(err error, op string, details map[string]any) error {
	var de *domainerrors.DomainError
	if errors.As(err, &de) {
		return de
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == statusNameIndex {
		return domainerrors.ConflictError("status name already exists", details).WithReason("STATUS_NAME_TAKEN")
	}
	return translateError(err, "status", op, details)
}

func toStatusRow(m *model.Status) *statusRow {
	return &statusRow{
		ID:        m.ID,
		ProjectID: m.ProjectID,
		Name:      m.Name,
		Category:  string(m.Category),
		Rank:      m.Rank,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func fromStatusRow(r *statusRow) *model.Status {
	return &model.Status{
		ID:        r.ID,
		ProjectID: r.ProjectID,
		Name:      r.Name,
		Category:  model.IssueState(r.Category),
		Rank:      r.Rank,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

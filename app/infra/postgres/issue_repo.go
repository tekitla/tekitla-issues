package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	domainerrors "github.com/vrojas1015/tequio-issues/app/domain/errors"
	"github.com/vrojas1015/tequio-issues/app/domain/model"
	"github.com/vrojas1015/tequio-issues/app/domain/port"
)

// issueRow es la fila de issues.issues. State se guarda como texto
// ('todo' | 'in_progress' | 'done') y Priority como entero 1..4 para poder
// ordenar por ella; ambos con CHECK en la migración.
type issueRow struct {
	ID          string    `gorm:"primaryKey;column:id;type:uuid"`
	ProjectID   string    `gorm:"column:project_id;type:uuid;not null"`
	Number      int32     `gorm:"column:number;not null"`
	Title       string    `gorm:"column:title;not null"`
	Description string    `gorm:"column:description;not null"`
	State       string    `gorm:"column:state;not null"`
	Priority    int       `gorm:"column:priority;not null"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (issueRow) TableName() string { return "issues.issues" }

// IssueRepository implementa port.IssueRepository sobre Postgres.
type IssueRepository struct{ db *gorm.DB }

var _ port.IssueRepository = (*IssueRepository)(nil)

// NewIssueRepository construye el repo.
func NewIssueRepository(db *gorm.DB) *IssueRepository {
	return &IssueRepository{db: db}
}

// errProjectNotOwned marca, dentro de la transacción de Create, que el
// UPDATE del proyecto no afectó filas (no existe o es de otro dueño).
var errProjectNotOwned = errors.New("project not found for owner")

// Create reserva el número y crea el issue en una sola transacción.
//
// La reserva es un UPDATE ... RETURNING sobre la fila del proyecto: toma un
// lock de fila, así dos CreateIssue concurrentes del mismo proyecto se
// serializan y nunca reciben el mismo número. Si el INSERT falla, el rollback
// devuelve el número (no queda hueco); como no hay borrado de issues, un
// número asignado nunca se reutiliza.
func (r *IssueRepository) Create(ctx context.Context, ownerUID string, issue *model.Issue) (*model.Issue, error) {
	row := toIssueRow(issue)
	now := dbNow()
	row.CreatedAt, row.UpdatedAt = now, now

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var number int32
		err := tx.Raw(
			`UPDATE issues.projects
			    SET next_issue_number = next_issue_number + 1
			  WHERE id = ? AND owner_uid = ?
			RETURNING next_issue_number - 1`,
			issue.ProjectID, ownerUID,
		).Row().Scan(&number)
		if errors.Is(err, sql.ErrNoRows) {
			return errProjectNotOwned
		}
		if err != nil {
			return err
		}
		row.Number = number
		return tx.Create(row).Error
	})
	if errors.Is(err, errProjectNotOwned) {
		return nil, domainerrors.NotFoundError("project not found", map[string]any{"id": issue.ProjectID})
	}
	if err != nil {
		return nil, translateError(err, "issue", "create", map[string]any{"project_id": issue.ProjectID})
	}
	return fromIssueRow(row), nil
}

// GetByNumber busca un issue por (project_id, number).
func (r *IssueRepository) GetByNumber(ctx context.Context, projectID string, number int32) (*model.Issue, error) {
	var row issueRow
	err := r.db.WithContext(ctx).Where("project_id = ? AND number = ?", projectID, number).First(&row).Error
	if err != nil {
		return nil, translateError(err, "issue", "get", map[string]any{"project_id": projectID, "number": number})
	}
	return fromIssueRow(&row), nil
}

// List pagina por keyset (priority DESC, created_at DESC, id DESC), con filtro
// opcional por estado. Pide limit+1 filas para saber si hay página siguiente.
func (r *IssueRepository) List(ctx context.Context, filter port.IssueFilter) (*port.IssuePage, error) {
	limit := int(filter.Limit)

	query := r.db.WithContext(ctx).Model(&issueRow{}).Where("project_id = ?", filter.ProjectID)
	if filter.State != model.IssueStateUnspecified {
		query = query.Where("state = ?", string(filter.State))
	}
	if filter.Cursor != "" {
		c, err := decodeIssueCursor(filter.Cursor)
		if err != nil {
			return nil, domainerrors.ValidationError("invalid cursor", map[string]any{"field": "cursor"})
		}
		// Todo DESC => el keyset usa "menor que" sobre la tupla completa.
		query = query.Where("(priority, created_at, id) < (?, ?, ?)", c.Priority, c.createdAt(), c.ID)
	}

	var rows []issueRow
	if err := query.Order("priority DESC, created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, translateError(err, "issues", "list", map[string]any{"project_id": filter.ProjectID})
	}

	hasNext := len(rows) > limit
	if hasNext {
		rows = rows[:limit]
	}
	page := &port.IssuePage{Issues: make([]*model.Issue, len(rows)), HasNext: hasNext}
	for i := range rows {
		page.Issues[i] = fromIssueRow(&rows[i])
	}
	if hasNext && len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = encodeCursor(issueCursor{
			Priority:  last.Priority,
			CreatedAt: last.CreatedAt.UnixMicro(),
			ID:        last.ID,
		})
	}
	return page, nil
}

// UpdateState cambia el estado (y updated_at) y devuelve la fila actualizada
// con RETURNING. Sin filas afectadas -> NotFound.
func (r *IssueRepository) UpdateState(ctx context.Context, projectID string, number int32, state model.IssueState) (*model.Issue, error) {
	var rows []issueRow
	res := r.db.WithContext(ctx).
		Model(&rows).
		Clauses(clause.Returning{}).
		Where("project_id = ? AND number = ?", projectID, number).
		Updates(map[string]any{"state": string(state), "updated_at": dbNow()})
	details := map[string]any{"project_id": projectID, "number": number}
	if res.Error != nil {
		return nil, translateError(res.Error, "issue", "update", details)
	}
	if res.RowsAffected == 0 || len(rows) == 0 {
		return nil, domainerrors.NotFoundError("issue not found", details)
	}
	return fromIssueRow(&rows[0]), nil
}

func toIssueRow(m *model.Issue) *issueRow {
	return &issueRow{
		ID:          m.ID,
		ProjectID:   m.ProjectID,
		Number:      m.Number,
		Title:       m.Title,
		Description: m.Description,
		State:       string(m.State),
		Priority:    int(m.Priority),
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

func fromIssueRow(r *issueRow) *model.Issue {
	return &model.Issue{
		ID:          r.ID,
		ProjectID:   r.ProjectID,
		Number:      r.Number,
		Title:       r.Title,
		Description: r.Description,
		State:       model.IssueState(r.State),
		Priority:    model.Priority(r.Priority),
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

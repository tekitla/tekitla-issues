package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// projectRow es la fila de issues.projects.
//
// NextIssueNumber es el número que recibirá el próximo issue del proyecto; es
// un detalle de persistencia (no está en el dominio) y solo lo incrementa
// IssueRepository.Create.
type projectRow struct {
	ID              string    `gorm:"primaryKey;column:id;type:uuid"`
	OwnerUID        string    `gorm:"column:owner_uid;not null"`
	Key             string    `gorm:"column:key;not null"`
	Name            string    `gorm:"column:name;not null"`
	Description     string    `gorm:"column:description;not null"`
	NextIssueNumber int32     `gorm:"column:next_issue_number;not null"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (projectRow) TableName() string { return "issues.projects" }

// ProjectRepository implementa port.ProjectRepository sobre Postgres.
type ProjectRepository struct{ db *gorm.DB }

var _ port.ProjectRepository = (*ProjectRepository)(nil)

// NewProjectRepository construye el repo.
func NewProjectRepository(db *gorm.DB) *ProjectRepository {
	return &ProjectRepository{db: db}
}

// Create inserta el proyecto con next_issue_number = 1. Key duplicada para el
// mismo dueño -> ConflictError (constraint projects_owner_uid_key_key).
func (r *ProjectRepository) Create(ctx context.Context, p *model.Project) (*model.Project, error) {
	row := toProjectRow(p)
	row.NextIssueNumber = 1
	now := dbNow()
	row.CreatedAt, row.UpdatedAt = now, now
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, translateError(err, "project", "create", map[string]any{"key": p.Key})
	}
	return fromProjectRow(row), nil
}

// GetByID busca el proyecto por PK, solo si es de ownerUID.
func (r *ProjectRepository) GetByID(ctx context.Context, ownerUID, id string) (*model.Project, error) {
	var row projectRow
	err := r.db.WithContext(ctx).Where("id = ? AND owner_uid = ?", id, ownerUID).First(&row).Error
	if err != nil {
		return nil, translateError(err, "project", "get", map[string]any{"id": id})
	}
	return fromProjectRow(&row), nil
}

// List pagina por keyset (name ASC, id ASC): pide limit+1 filas para saber si
// hay página siguiente sin un COUNT.
func (r *ProjectRepository) List(ctx context.Context, ownerUID string, filter port.PageFilter) (*port.ProjectPage, error) {
	limit := int(filter.Limit)

	query := r.db.WithContext(ctx).Model(&projectRow{}).Where("owner_uid = ?", ownerUID)
	if filter.Cursor != "" {
		c, err := decodeProjectCursor(filter.Cursor)
		if err != nil {
			return nil, domainerrors.ValidationError("invalid cursor", map[string]any{"field": "cursor"})
		}
		// Orden ASC => el keyset usa "mayor que".
		query = query.Where("(name, id) > (?, ?)", c.Name, c.ID)
	}

	var rows []projectRow
	if err := query.Order("name ASC, id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, translateError(err, "projects", "list", nil)
	}

	hasNext := len(rows) > limit
	if hasNext {
		rows = rows[:limit]
	}
	page := &port.ProjectPage{Projects: make([]*model.Project, len(rows)), HasNext: hasNext}
	for i := range rows {
		page.Projects[i] = fromProjectRow(&rows[i])
	}
	if hasNext && len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = encodeCursor(projectCursor{Name: last.Name, ID: last.ID})
	}
	return page, nil
}

// dbNow es el "ahora" truncado a microsegundos (la precisión de timestamptz),
// para que lo que devuelve un Create sea idéntico a lo que se lee después y
// los cursores keyset coincidan con lo guardado.
func dbNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func toProjectRow(m *model.Project) *projectRow {
	return &projectRow{
		ID:          m.ID,
		OwnerUID:    m.OwnerUID,
		Key:         m.Key,
		Name:        m.Name,
		Description: m.Description,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

func fromProjectRow(r *projectRow) *model.Project {
	return &model.Project{
		ID:          r.ID,
		OwnerUID:    r.OwnerUID,
		Key:         r.Key,
		Name:        r.Name,
		Description: r.Description,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

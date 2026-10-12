package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// issueRow es la fila de issues.issues. State se guarda como texto
// ('todo' | 'in_progress' | 'done' | 'canceled') y Priority como entero 1..4;
// ambos con CHECK en la migración. Rank es TEXT COLLATE "C": Postgres lo
// ordena byte a byte, igual que model.RankBetween.
type issueRow struct {
	ID          string `gorm:"primaryKey;column:id;type:uuid"`
	ProjectID   string `gorm:"column:project_id;type:uuid;not null"`
	Number      int32  `gorm:"column:number;not null"`
	Title       string `gorm:"column:title;not null"`
	Description string `gorm:"column:description;not null"`
	State       string `gorm:"column:state;not null"`
	Priority    int    `gorm:"column:priority;not null"`
	Rank        string `gorm:"column:rank;not null"`
	StatusID    string `gorm:"column:status_id;type:uuid;not null"`
	// Motivo del descarte: las tres juntas o ninguna, y solo en canceled
	// (CHECKs de 000004).
	DiscardReason *string    `gorm:"column:discard_reason"`
	DiscardedBy   *string    `gorm:"column:discarded_by"`
	DiscardedAt   *time.Time `gorm:"column:discarded_at"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
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
// UPDATE del proyecto no afectó filas (no existe o es de otro espacio).
var errProjectNotOwned = errors.New("project not found in workspaces")

// Create reserva el número, calcula el rank y crea el issue en una sola
// transacción.
//
// La reserva es un UPDATE ... RETURNING sobre la fila del proyecto: toma un
// lock de fila, así dos CreateIssue concurrentes del mismo proyecto se
// serializan y nunca reciben el mismo número ni el mismo rank (el rank nuevo
// sale del mayor rank del proyecto, leído con el lock tomado). Si el INSERT
// falla, el rollback devuelve el número (no queda hueco); como no hay borrado
// de issues, un número asignado nunca se reutiliza.
func (r *IssueRepository) Create(ctx context.Context, workspaceIDs []string, issue *model.Issue, actorUID string) (*model.Issue, error) {
	row := toIssueRow(issue)
	now := dbNow()
	row.CreatedAt, row.UpdatedAt = now, now

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var number int32
		err := tx.Raw(
			`UPDATE issues.projects
			    SET next_issue_number = next_issue_number + 1
			  WHERE id = ? AND workspace_id IN ?
			RETURNING next_issue_number - 1`,
			issue.ProjectID, workspaceIDs,
		).Row().Scan(&number)
		if errors.Is(err, sql.ErrNoRows) {
			return errProjectNotOwned
		}
		if err != nil {
			return err
		}

		var last []string
		err = tx.Model(&issueRow{}).Where("project_id = ?", issue.ProjectID).
			Order("rank DESC").Limit(1).Pluck("rank", &last).Error
		if err != nil {
			return err
		}
		lastRank := ""
		if len(last) == 1 {
			lastRank = last[0]
		}
		rank, err := model.RankAfter(lastRank)
		if err != nil {
			// Un rank guardado mal formado es un problema de datos, no del cliente.
			return fmt.Errorf("rank after %q: %v", lastRank, err)
		}

		// El estado lo eligió el service antes de la transacción: con el lock
		// del proyecto tomado, se verifica que siga existiendo.
		if _, err := getStatusRow(tx, issue.ProjectID, issue.StatusID); errors.Is(err, gorm.ErrRecordNotFound) {
			return statusGone(issue.StatusID)
		} else if err != nil {
			return err
		}

		row.Number = number
		row.Rank = rank
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		return insertEvent(tx, row.ID, actorUID, &model.IssueEvent{Kind: model.EventCreated}, now)
	})
	if errors.Is(err, errProjectNotOwned) {
		return nil, domainerrors.NotFoundError("project not found", map[string]any{"id": issue.ProjectID})
	}
	if err != nil {
		return nil, writeError(err, "issue", "create", map[string]any{"project_id": issue.ProjectID})
	}
	out := fromIssueRow(row)
	out.LabelIDs = []string{}
	return out, nil
}

// GetByNumber busca un issue por (project_id, number).
func (r *IssueRepository) GetByNumber(ctx context.Context, projectID string, number int32) (*model.Issue, error) {
	issue, err := getIssue(r.db.WithContext(ctx), projectID, number)
	if err != nil {
		return nil, translateError(err, "issue", "get", map[string]any{"project_id": projectID, "number": number})
	}
	return issue, nil
}

// List pagina por keyset con los filtros y el orden de filter (empates por
// rank, único por proyecto). Pide limit+1 filas para saber si hay página
// siguiente. El cursor lleva la huella de los filtros y el orden: uno de otra
// consulta (o de una versión anterior) es INVALID_ARGUMENT y el cliente
// vuelve a la primera página.
func (r *IssueRepository) List(ctx context.Context, filter port.IssueFilter) (*port.IssuePage, error) {
	limit := int(filter.Limit)
	fp := issueFilterFingerprint(filter)

	query := r.db.WithContext(ctx).Table("issues.issues AS i").Select("i.*").Where("i.project_id = ?", filter.ProjectID)
	if len(filter.States) > 0 {
		states := make([]string, len(filter.States))
		for i, st := range filter.States {
			states[i] = string(st)
		}
		query = query.Where("i.state IN ?", states)
	}
	if len(filter.StatusIDs) > 0 {
		query = query.Where("i.status_id IN ?", filter.StatusIDs)
	}
	if len(filter.Priorities) > 0 {
		priorities := make([]int, len(filter.Priorities))
		for i, p := range filter.Priorities {
			priorities[i] = int(p)
		}
		query = query.Where("i.priority IN ?", priorities)
	}
	if len(filter.LabelIDs) > 0 {
		query = query.Where("EXISTS (SELECT 1 FROM issues.issue_labels il WHERE il.issue_id = i.id AND il.label_id IN ?)", filter.LabelIDs)
	}
	if s := filter.Search; s.Text != "" {
		// La misma función a los dos lados: sin mayúsculas ni acentos. El texto
		// va escapado para LIKE (el escape por defecto de Postgres es \).
		match := "issues.search_fold(i.title) LIKE '%' || issues.search_fold(?) || '%'"
		if s.Number > 0 {
			query = query.Where("("+match+" OR i.number = ?)", escapeLike(s.Text), s.Number)
		} else {
			query = query.Where(match, escapeLike(s.Text))
		}
	}
	if filter.Cursor != "" {
		c, err := decodeIssueCursor(filter.Cursor)
		if err != nil || c.Filter != fp {
			return nil, domainerrors.ValidationError("invalid cursor", map[string]any{"field": "cursor"})
		}
		// Keyset: después en la columna del orden, o igual y con rank mayor.
		switch filter.Order {
		case model.IssueOrderUpdatedDesc:
			at := time.UnixMicro(c.UpdatedMicros).UTC()
			query = query.Where("(i.updated_at < ? OR (i.updated_at = ? AND i.rank > ?))", at, at, c.Rank)
		case model.IssueOrderPriorityDesc:
			query = query.Where("(i.priority < ? OR (i.priority = ? AND i.rank > ?))", c.Priority, c.Priority, c.Rank)
		default:
			query = query.Where("i.rank > ?", c.Rank)
		}
	}
	switch filter.Order {
	case model.IssueOrderUpdatedDesc:
		query = query.Order("i.updated_at DESC").Order("i.rank ASC")
	case model.IssueOrderPriorityDesc:
		query = query.Order("i.priority DESC").Order("i.rank ASC")
	default:
		query = query.Order("i.rank ASC")
	}

	var rows []issueRow
	if err := query.Limit(limit + 1).Find(&rows).Error; err != nil {
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
	if err := attachLabels(r.db.WithContext(ctx), page.Issues); err != nil {
		return nil, translateError(err, "issues", "list", map[string]any{"project_id": filter.ProjectID})
	}
	if hasNext && len(rows) > 0 {
		last := rows[len(rows)-1]
		c := issueCursor{Rank: last.Rank, Filter: fp}
		switch filter.Order {
		case model.IssueOrderUpdatedDesc:
			c.UpdatedMicros = last.UpdatedAt.UnixMicro()
		case model.IssueOrderPriorityDesc:
			c.Priority = last.Priority
		}
		page.NextCursor = encodeCursor(c)
	}
	return page, nil
}

// likeEscaper escapa los comodines de LIKE (y el propio escape) para buscar
// el texto literal.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLike(s string) string { return likeEscaper.Replace(s) }

func toIssueRow(m *model.Issue) *issueRow {
	row := &issueRow{
		ID:          m.ID,
		ProjectID:   m.ProjectID,
		Number:      m.Number,
		Title:       m.Title,
		Description: m.Description,
		State:       string(m.State),
		Priority:    int(m.Priority),
		Rank:        m.Rank,
		StatusID:    m.StatusID,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
	if d := m.Discard; d != nil {
		row.DiscardReason, row.DiscardedBy, row.DiscardedAt = &d.Reason, &d.ActorUID, &d.DiscardedAt
	}
	return row
}

func fromIssueRow(r *issueRow) *model.Issue {
	issue := &model.Issue{
		ID:          r.ID,
		ProjectID:   r.ProjectID,
		Number:      r.Number,
		Title:       r.Title,
		Description: r.Description,
		State:       model.IssueState(r.State),
		Priority:    model.Priority(r.Priority),
		Rank:        r.Rank,
		StatusID:    r.StatusID,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
	if r.DiscardReason != nil && r.DiscardedBy != nil && r.DiscardedAt != nil {
		issue.Discard = &model.DiscardInfo{Reason: *r.DiscardReason, ActorUID: *r.DiscardedBy, DiscardedAt: *r.DiscardedAt}
	}
	return issue
}

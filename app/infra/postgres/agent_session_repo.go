package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// agentSessionRow es la fila de issues.agent_sessions (issue 026, 000009).
// AgentDisplayName no es columna: sale del JOIN con issues.actors al leer.
type agentSessionRow struct {
	ID               string     `gorm:"primaryKey;column:id;type:uuid"`
	OwnerActorID     string     `gorm:"column:owner_actor_id;type:uuid"`
	AgentActorID     string     `gorm:"column:agent_actor_id;type:uuid"`
	AgentDisplayName string     `gorm:"column:agent_display_name;->"`
	Source           string     `gorm:"column:source"`
	Machine          string     `gorm:"column:machine"`
	ExternalID       string     `gorm:"column:external_id"`
	Tool             string     `gorm:"column:tool"`
	CwdRel           string     `gorm:"column:cwd_rel"`
	SessionName      string     `gorm:"column:session_name"`
	WorkspaceLabel   string     `gorm:"column:workspace_label"`
	ProjectID        *string    `gorm:"column:project_id;type:uuid"`
	IssueNumber      *int32     `gorm:"column:issue_number"`
	Link             string     `gorm:"column:link"`
	Status           string     `gorm:"column:status"`
	WaitingOn        string     `gorm:"column:waiting_on"`
	WaitingTarget    string     `gorm:"column:waiting_target"`
	WaitingText      string     `gorm:"column:waiting_text"`
	WaitingReason    string     `gorm:"column:waiting_reason"`
	StatusChangedAt  time.Time  `gorm:"column:status_changed_at"`
	LastSeenAt       time.Time  `gorm:"column:last_seen_at"`
	StartedAt        time.Time  `gorm:"column:started_at"`
	EndedAt          *time.Time `gorm:"column:ended_at"`
}

func (agentSessionRow) TableName() string { return "issues.agent_sessions" }

// sessionSelect lee una sesión con el nombre de su agente.
const sessionSelect = `SELECT s.*, a.display_name AS agent_display_name
  FROM issues.agent_sessions s JOIN issues.actors a ON a.id = s.agent_actor_id`

// Columnas que pisa un reporte sobre una sesión existente (no id, dueño,
// origen, máquina ni external_id: son la clave).
var sessionUpsertColumns = []string{
	"agent_actor_id", "tool", "cwd_rel", "session_name", "workspace_label", "project_id",
	"issue_number", "link", "status", "waiting_on", "waiting_target", "waiting_text",
	"waiting_reason", "status_changed_at", "last_seen_at", "started_at", "ended_at",
}

// sessionCursor: orden (last_seen_at DESC, id DESC).
type sessionCursor struct {
	SeenMicros int64  `json:"s"`
	ID         string `json:"i"`
}

// AgentSessionRepository implementa port.AgentSessionRepository sobre
// Postgres.
type AgentSessionRepository struct{ db *gorm.DB }

var _ port.AgentSessionRepository = (*AgentSessionRepository)(nil)

// NewAgentSessionRepository construye el repo.
func NewAgentSessionRepository(db *gorm.DB) *AgentSessionRepository {
	return &AgentSessionRepository{db: db}
}

// ProjectIDsByKey resuelve claves (ya en mayúsculas: las keys se guardan
// así) entre los proyectos de los espacios del usuario.
func (r *AgentSessionRepository) ProjectIDsByKey(ctx context.Context, workspaceIDs []string, keys []string) (map[string]string, error) {
	out := map[string]string{}
	if len(workspaceIDs) == 0 || len(keys) == 0 {
		return out, nil
	}
	var rows []struct {
		ID  string
		Key string
	}
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, key FROM issues.projects WHERE workspace_id IN ? AND key IN ?`, workspaceIDs, keys,
	).Scan(&rows).Error
	if err != nil {
		return nil, translateError(err, "projects", "list", nil)
	}
	for _, row := range rows {
		out[row.Key] = row.ID
	}
	return out, nil
}

// FindAgent busca el agente por dueño, herramienta y nombre sin
// mayúsculas; si hay varios (revocados con el mismo nombre), primero el sin
// revocar y después el más nuevo.
func (r *AgentSessionRepository) FindAgent(ctx context.Context, ownerID string, tool model.AgentTool, name string) (*model.Actor, error) {
	var row actorRow
	err := r.db.WithContext(ctx).
		Where("owner_actor_id = ? AND kind = 'agent' AND tool = ? AND lower(display_name) = lower(?)", ownerID, string(tool), name).
		Order("revoked_at IS NOT NULL, created_at DESC").
		First(&row).Error
	if err != nil {
		return nil, translateError(err, "agent", "get", map[string]any{"owner_actor_id": ownerID, "tool": string(tool)})
	}
	return fromActorRow(&row), nil
}

// Report aplica el reporte con un advisory lock de transacción sobre la
// clave: dos reportes de la misma máquina no se cruzan.
func (r *AgentSessionRepository) Report(ctx context.Context, key model.SessionKey, now time.Time, plan func(existing []*model.AgentSession, history map[string]bool) model.ReportOutcome) (model.ReportOutcome, error) {
	details := map[string]any{"machine": key.Machine}
	var out model.ReportOutcome
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 26))`,
			key.OwnerActorID+"|"+string(key.Source)+"|"+key.Machine).Error; err != nil {
			return err
		}
		var rows []agentSessionRow
		if err := tx.Raw(sessionSelect+` WHERE s.owner_actor_id = ? AND s.source = ? AND s.machine = ?`,
			key.OwnerActorID, string(key.Source), key.Machine).Scan(&rows).Error; err != nil {
			return err
		}
		existing := make([]*model.AgentSession, len(rows))
		for i := range rows {
			existing[i] = fromSessionRow(&rows[i])
		}
		history := map[string]bool{}
		if len(rows) > 0 {
			ids := make([]string, len(rows))
			for i := range rows {
				ids[i] = rows[i].ID
			}
			var withHistory []string
			if err := tx.Raw(`SELECT s.id FROM unnest(ARRAY[?]::uuid[]) AS s(id)
				       WHERE EXISTS (SELECT 1 FROM issues.agent_session_transitions t WHERE t.session_id = s.id)`, ids).
				Scan(&withHistory).Error; err != nil {
				return err
			}
			for _, id := range withHistory {
				history[id] = true
			}
		}
		out = plan(existing, history)
		if len(out.Upserts) > 0 {
			// Un solo INSERT … ON CONFLICT para todo el reporte (hasta 200).
			rows := make([]*agentSessionRow, len(out.Upserts))
			for i, s := range out.Upserts {
				rows[i] = toSessionRow(s)
			}
			err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "owner_actor_id"}, {Name: "source"}, {Name: "machine"}, {Name: "external_id"}},
				DoUpdates: clause.AssignmentColumns(sessionUpsertColumns),
			}).Omit("agent_display_name").Create(&rows).Error
			if err != nil {
				return err
			}
		}
		if len(out.Transitions) > 0 {
			trs := make([]*sessionTransitionRow, len(out.Transitions))
			for i, t := range out.Transitions {
				trs[i] = toTransitionRow(t)
			}
			if err := tx.Omit("seq").Create(&trs).Error; err != nil {
				return err
			}
		}
		if len(out.EndedIDs) > 0 {
			err := tx.Exec(
				`UPDATE issues.agent_sessions
				    SET status = 'ended', ended_at = ?, status_changed_at = ?,
				        waiting_on = 'none', waiting_target = '', waiting_text = '', waiting_reason = ''
				  WHERE id IN ? AND status <> 'ended'`,
				now, now, out.EndedIDs,
			).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return model.ReportOutcome{}, translateError(err, "agent sessions", "report", details)
	}
	return out, nil
}

// List devuelve una página de sesiones de la persona.
func (r *AgentSessionRepository) List(ctx context.Context, f port.AgentSessionFilter) (*port.AgentSessionPage, error) {
	q := sessionSelect + ` WHERE s.owner_actor_id = ?`
	args := []any{f.OwnerActorID}
	if f.ProjectID != "" {
		q += ` AND s.project_id = ?`
		args = append(args, f.ProjectID)
	}
	if f.IssueNumber > 0 {
		q += ` AND s.issue_number = ?`
		args = append(args, f.IssueNumber)
	}
	if f.Unlinked {
		q += ` AND s.project_id IS NULL`
	}
	if f.AgentActorID != "" {
		q += ` AND s.agent_actor_id = ?`
		args = append(args, f.AgentActorID)
	}
	if len(f.Statuses) > 0 || f.Working || f.Stalled {
		// El estado efectivo en SQL: así el LIMIT y el cursor cuentan solo
		// las filas que se devuelven.
		var ors []string
		if len(f.Statuses) > 0 {
			statuses := make([]string, len(f.Statuses))
			for i, s := range f.Statuses {
				statuses[i] = string(s)
			}
			ors = append(ors, `s.status IN ?`)
			args = append(args, statuses)
		}
		if f.Working {
			ors = append(ors, `(s.status = 'working' AND s.last_seen_at >= ?)`)
			args = append(args, f.StallBefore)
		}
		if f.Stalled {
			ors = append(ors, `(s.status = 'working' AND s.last_seen_at < ?)`)
			args = append(args, f.StallBefore)
		}
		q += ` AND (` + strings.Join(ors, " OR ") + `)`
	}
	if !f.ActiveSince.IsZero() {
		q += ` AND s.status <> 'ended' AND s.last_seen_at >= ?`
		args = append(args, f.ActiveSince)
	}
	if f.Page.Cursor != "" {
		var c sessionCursor
		if err := decodeCursor(f.Page.Cursor, &c); err != nil || uuid.Validate(c.ID) != nil {
			return nil, domainerrors.ValidationError("invalid cursor", map[string]any{"field": "page.cursor"})
		}
		q += ` AND (s.last_seen_at, s.id) < (?, ?)`
		args = append(args, time.UnixMicro(c.SeenMicros).UTC(), c.ID)
	}
	q += ` ORDER BY s.last_seen_at DESC, s.id DESC LIMIT ?`
	args = append(args, f.Page.Limit+1)
	var rows []agentSessionRow
	if err := r.db.WithContext(ctx).Raw(q, args...).Scan(&rows).Error; err != nil {
		return nil, translateError(err, "agent sessions", "list", nil)
	}
	page := &port.AgentSessionPage{}
	if int32(len(rows)) > f.Page.Limit {
		rows = rows[:f.Page.Limit]
		last := rows[len(rows)-1]
		page.HasNext = true
		page.NextCursor = encodeCursor(sessionCursor{SeenMicros: last.LastSeenAt.UnixMicro(), ID: last.ID})
	}
	page.Sessions = make([]*model.AgentSession, len(rows))
	for i := range rows {
		page.Sessions[i] = fromSessionRow(&rows[i])
	}
	return page, nil
}

// ListForIssues usa el índice parcial (project_id, issue_number).
func (r *AgentSessionRepository) ListForIssues(ctx context.Context, projectID string, numbers []int32, activeSince time.Time) ([]*model.AgentSession, error) {
	if len(numbers) == 0 {
		return nil, nil
	}
	var rows []agentSessionRow
	err := r.db.WithContext(ctx).Raw(
		sessionSelect+` WHERE s.project_id = ? AND s.issue_number IN ? AND s.status <> 'ended' AND s.last_seen_at >= ?`,
		projectID, numbers, activeSince,
	).Scan(&rows).Error
	if err != nil {
		return nil, translateError(err, "agent sessions", "list", map[string]any{"project_id": projectID})
	}
	out := make([]*model.AgentSession, len(rows))
	for i := range rows {
		out[i] = fromSessionRow(&rows[i])
	}
	return out, nil
}

func toSessionRow(m *model.AgentSession) *agentSessionRow {
	row := &agentSessionRow{
		ID:              m.ID,
		OwnerActorID:    m.OwnerActorID,
		AgentActorID:    m.AgentActorID,
		Source:          string(m.Source),
		Machine:         m.Machine,
		ExternalID:      m.ExternalID,
		Tool:            string(m.Tool),
		CwdRel:          m.CwdRel,
		SessionName:     m.SessionName,
		WorkspaceLabel:  m.WorkspaceLabel,
		Link:            string(m.Link),
		Status:          string(m.Status),
		WaitingOn:       string(m.WaitingOn),
		WaitingTarget:   m.WaitingTarget,
		WaitingText:     m.WaitingText,
		WaitingReason:   string(m.WaitingReason),
		StatusChangedAt: m.StatusChangedAt,
		LastSeenAt:      m.LastSeenAt,
		StartedAt:       m.StartedAt,
		EndedAt:         m.EndedAt,
	}
	if m.ProjectID != "" {
		pid, n := m.ProjectID, m.IssueNumber
		row.ProjectID, row.IssueNumber = &pid, &n
	}
	return row
}

func fromSessionRow(r *agentSessionRow) *model.AgentSession {
	m := &model.AgentSession{
		ID:               r.ID,
		OwnerActorID:     r.OwnerActorID,
		AgentActorID:     r.AgentActorID,
		AgentDisplayName: r.AgentDisplayName,
		Source:           model.SessionSource(r.Source),
		Machine:          r.Machine,
		ExternalID:       r.ExternalID,
		Tool:             model.AgentTool(r.Tool),
		CwdRel:           r.CwdRel,
		SessionName:      r.SessionName,
		WorkspaceLabel:   r.WorkspaceLabel,
		Link:             model.SessionLink(r.Link),
		Status:           model.SessionStatus(r.Status),
		WaitingOn:        model.WaitingOn(r.WaitingOn),
		WaitingTarget:    r.WaitingTarget,
		WaitingText:      r.WaitingText,
		WaitingReason:    model.WaitingReason(r.WaitingReason),
		StatusChangedAt:  r.StatusChangedAt.UTC(),
		LastSeenAt:       r.LastSeenAt.UTC(),
		StartedAt:        r.StartedAt.UTC(),
	}
	if r.ProjectID != nil {
		m.ProjectID = *r.ProjectID
	}
	if r.IssueNumber != nil {
		m.IssueNumber = *r.IssueNumber
	}
	if r.EndedAt != nil {
		t := r.EndedAt.UTC()
		m.EndedAt = &t
	}
	return m
}

// sessionTransitionRow es una fila de issues.agent_session_transitions
// (issue 036, 000011): la historia de una sesión.
type sessionTransitionRow struct {
	SessionID     string    `gorm:"column:session_id;type:uuid"`
	Seq           int64     `gorm:"column:seq"`
	At            time.Time `gorm:"column:at"`
	Status        string    `gorm:"column:status"`
	WaitingOn     string    `gorm:"column:waiting_on"`
	WaitingReason string    `gorm:"column:waiting_reason"`
	ProjectID     *string   `gorm:"column:project_id;type:uuid"`
	IssueNumber   *int32    `gorm:"column:issue_number"`
}

func (sessionTransitionRow) TableName() string { return "issues.agent_session_transitions" }

func toTransitionRow(t model.SessionTransition) *sessionTransitionRow {
	row := &sessionTransitionRow{
		SessionID: t.SessionID, At: t.At, Status: string(t.Status),
		WaitingOn: string(t.WaitingOn), WaitingReason: string(t.WaitingReason),
	}
	if t.ProjectID != "" {
		pid, n := t.ProjectID, t.IssueNumber
		row.ProjectID, row.IssueNumber = &pid, &n
	}
	return row
}

func fromTransitionRow(r *sessionTransitionRow) model.SessionTransition {
	t := model.SessionTransition{
		SessionID: r.SessionID, At: r.At.UTC(), Status: model.SessionStatus(r.Status),
		WaitingOn: model.WaitingOn(r.WaitingOn), WaitingReason: model.WaitingReason(r.WaitingReason),
	}
	if r.ProjectID != nil {
		t.ProjectID = *r.ProjectID
	}
	if r.IssueNumber != nil {
		t.IssueNumber = *r.IssueNumber
	}
	return t
}

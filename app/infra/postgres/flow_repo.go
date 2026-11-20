package postgres

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// FlowRepository implementa port.FlowRepository sobre Postgres. Lee todo el
// proyecto en tres consultas y el cálculo se hace en memoria: con cientos de
// issues y miles de eventos alcanza (issue 036).
type FlowRepository struct{ db *gorm.DB }

var _ port.FlowRepository = (*FlowRepository)(nil)

// NewFlowRepository construye el repo.
func NewFlowRepository(db *gorm.DB) *FlowRepository {
	return &FlowRepository{db: db}
}

// Kinds de evento que cambian la categoría del issue.
var transitionKinds = []string{string(model.EventStatusChanged), string(model.EventDiscarded), string(model.EventRestored)}

// dataKinds: los eventos cuyos datos lee el cálculo (las transiciones y el
// alta, que guarda el estado en el que nace desde el issue 033).
var dataKinds = append([]string{string(model.EventCreated)}, transitionKinds...)

// ListFlowIssues: ver port.FlowRepository.
func (r *FlowRepository) ListFlowIssues(ctx context.Context, projectID string, labelIDs []string) ([]model.FlowIssue, error) {
	details := map[string]any{"project_id": projectID}
	db := r.db.WithContext(ctx)

	// Con labelIDs, las tres consultas se limitan a los issues que tienen
	// alguna (mismo filtro), así no se leen eventos que después se descartan.
	scope := func(q *gorm.DB) *gorm.DB {
		q = q.Where("i.project_id = ?", projectID)
		if len(labelIDs) > 0 {
			q = q.Where("EXISTS (SELECT 1 FROM issues.issue_labels f WHERE f.issue_id = i.id AND f.label_id IN ?)", labelIDs)
		}
		return q
	}
	q := scope(db.Table("issues.issues AS i").
		Select("i.id, i.number, i.title, i.status_id, i.state, i.created_at"))
	var rows []struct {
		ID        string
		Number    int32
		Title     string
		StatusID  string
		State     string
		CreatedAt time.Time
	}
	if err := q.Order("i.number").Scan(&rows).Error; err != nil {
		return nil, translateError(err, "flow", "list issues", details)
	}
	out := make([]model.FlowIssue, len(rows))
	index := make(map[string]int, len(rows))
	for i, row := range rows {
		out[i] = model.FlowIssue{
			Number: row.Number, Title: row.Title, StatusID: row.StatusID,
			State: model.IssueState(row.State), CreatedAt: row.CreatedAt,
		}
		index[row.ID] = i
	}
	if len(rows) == 0 {
		return out, nil
	}

	var pairs []struct {
		IssueID string
		LabelID string
	}
	err := scope(db.Table("issues.issue_labels AS il").
		Select("il.issue_id, il.label_id").
		Joins("JOIN issues.issues i ON i.id = il.issue_id")).
		Scan(&pairs).Error
	if err != nil {
		return nil, translateError(err, "flow", "list labels", details)
	}
	for _, p := range pairs {
		if k, ok := index[p.IssueID]; ok {
			out[k].LabelIDs = append(out[k].LabelIDs, p.LabelID)
		}
	}

	// Solo el detalle de los cambios de categoría: el resto cuenta como
	// actividad y no hace falta su JSONB.
	var evs []struct {
		IssueID   string
		Kind      string
		Data      []byte
		CreatedAt time.Time
		Source    *string
	}
	err = scope(db.Table("issues.issue_events AS e").
		Select("e.issue_id, e.kind, CASE WHEN e.kind IN ? THEN e.data END AS data, e.created_at, e.source", dataKinds).
		Joins("JOIN issues.issues i ON i.id = e.issue_id")).
		Order("e.issue_id, e.created_at, e.seq").
		Scan(&evs).Error
	if err != nil {
		return nil, translateError(err, "flow", "list events", details)
	}
	for _, e := range evs {
		k, ok := index[e.IssueID]
		if !ok {
			continue
		}
		ev := model.FlowEvent{At: e.CreatedAt, Created: e.Kind == string(model.EventCreated)}
		if e.Source != nil {
			ev.Source = model.EventSource(*e.Source)
		}
		if ev.Created && e.Data != nil {
			born, err := decodeCreatedData(e.Data)
			if err != nil {
				return nil, domainerrors.InternalError("failed to read event data", map[string]any{"cause": err.Error(), "issue_id": e.IssueID})
			}
			if born != nil {
				ev.Born = born.ToCategory
			}
		} else if e.Data != nil {
			var d statusChangeData
			if err := json.Unmarshal(e.Data, &d); err != nil {
				return nil, domainerrors.InternalError("failed to read event data", map[string]any{"cause": err.Error(), "issue_id": e.IssueID})
			}
			ev.Transition = true
			ev.From, ev.To = model.IssueState(d.FromCategory), model.IssueState(d.ToCategory)
		}
		out[k].Events = append(out[k].Events, ev)
	}
	return out, nil
}

// ListSessionTracks: ver port.FlowRepository. Dos consultas: las sesiones
// que alguna vez se ligaron al proyecto (o al issue) y toda su historia
// anterior a to (índice agent_session_transitions_issue_idx y la PK).
func (r *FlowRepository) ListSessionTracks(ctx context.Context, projectID string, number int32, to time.Time) ([]model.SessionTrack, error) {
	details := map[string]any{"project_id": projectID}
	db := r.db.WithContext(ctx)
	q := `SELECT s.id, s.last_seen_at FROM issues.agent_sessions s
	       WHERE EXISTS (SELECT 1 FROM issues.agent_session_transitions t
	                      WHERE t.session_id = s.id AND t.project_id = ?`
	args := []any{projectID}
	if number > 0 {
		q += ` AND t.issue_number = ?`
		args = append(args, number)
	}
	q += `) ORDER BY s.id`
	var sessions []struct {
		ID         string
		LastSeenAt time.Time
	}
	if err := db.Raw(q, args...).Scan(&sessions).Error; err != nil {
		return nil, translateError(err, "flow", "list sessions", details)
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	ids := make([]string, len(sessions))
	tracks := make([]model.SessionTrack, len(sessions))
	index := make(map[string]int, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
		tracks[i] = model.SessionTrack{SessionID: s.ID, LastSeenAt: s.LastSeenAt.UTC()}
		index[s.ID] = i
	}
	var rows []sessionTransitionRow
	err := db.Raw(`SELECT * FROM issues.agent_session_transitions
	                WHERE session_id IN ? AND at < ? ORDER BY session_id, at, seq`, ids, to).Scan(&rows).Error
	if err != nil {
		return nil, translateError(err, "flow", "list session transitions", details)
	}
	for i := range rows {
		k := index[rows[i].SessionID]
		tracks[k].Transitions = append(tracks[k].Transitions, fromTransitionRow(&rows[i]))
	}
	return tracks, nil
}

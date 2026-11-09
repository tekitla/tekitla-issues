package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// Escrituras de IssueRepository que registran historial (Move, Update,
// AddComment), su lectura (ListEvents) y los helpers compartidos.

// commentRow es la fila de issues.comments.
type commentRow struct {
	ID        string    `gorm:"primaryKey;column:id;type:uuid"`
	IssueID   string    `gorm:"column:issue_id;type:uuid;not null"`
	AuthorUID string    `gorm:"column:author_uid;not null"`
	Body      string    `gorm:"column:body;not null"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (commentRow) TableName() string { return "issues.comments" }

// eventRow es una fila de issues.issue_events leída con su comentario (LEFT
// JOIN). Data es el JSONB con el detalle según Kind (DTOs de abajo).
type eventRow struct {
	ID               string
	Seq              int64
	IssueID          string
	ActorUID         string
	Kind             string
	Data             []byte
	CreatedAt        time.Time
	CommentID        *string
	CommentAuthorUID *string
	CommentBody      *string
	CommentCreatedAt *time.Time
}

// DTOs del JSONB de issue_events.data, separados del dominio. Cambiarlos
// rompe la lectura del historial ya guardado: solo agregar campos.
type statusChangeData struct {
	FromID       string `json:"from_id"`
	FromName     string `json:"from_name"`
	FromCategory string `json:"from_category"`
	ToID         string `json:"to_id"`
	ToName       string `json:"to_name"`
	ToCategory   string `json:"to_category"`
}

// discardedData: el cambio de estado de DiscardIssue y su motivo. Restored
// usa statusChangeData.
type discardedData struct {
	statusChangeData
	Reason string `json:"reason"`
}

type titleChangeData struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type priorityChangeData struct {
	From int `json:"from"`
	To   int `json:"to"`
}

type labelRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type labelsChangeData struct {
	Added   []labelRef `json:"added"`
	Removed []labelRef `json:"removed"`
}

// eventCursor: orden (seq ASC); seq es único.
type eventCursor struct {
	Seq int64 `json:"s"`
}

// insertEvent guarda un evento del issue. El comentario, si lo hay, ya tiene
// que estar insertado.
func insertEvent(tx *gorm.DB, issueID, actorUID string, ev *model.IssueEvent, at time.Time) error {
	data, err := eventData(ev)
	if err != nil {
		return err
	}
	var commentID *string
	if ev.Comment != nil {
		commentID = &ev.Comment.ID
	}
	return tx.Exec(
		`INSERT INTO issues.issue_events (id, issue_id, actor_uid, kind, data, comment_id, created_at)
		 VALUES (?, ?, ?, ?, ?::jsonb, ?, ?)`,
		uuid.NewString(), issueID, actorUID, string(ev.Kind), string(data), commentID, at,
	).Error
}

func eventData(ev *model.IssueEvent) ([]byte, error) {
	var v any = struct{}{}
	switch ev.Kind {
	case model.EventStatusChanged, model.EventRestored:
		v = toStatusChangeData(ev.Status)
	case model.EventDiscarded:
		v = discardedData{statusChangeData: toStatusChangeData(ev.Status), Reason: ev.Reason}
	case model.EventTitleChanged:
		v = titleChangeData{From: ev.Title.From, To: ev.Title.To}
	case model.EventPriorityChanged:
		v = priorityChangeData{From: int(ev.Priority.From), To: int(ev.Priority.To)}
	case model.EventLabelsChanged:
		v = labelsChangeData{Added: labelRefs(ev.Labels.Added), Removed: labelRefs(ev.Labels.Removed)}
	}
	return json.Marshal(v)
}

func toStatusChangeData(c *model.StatusChange) statusChangeData {
	return statusChangeData{
		FromID: c.FromID, FromName: c.FromName, FromCategory: string(c.FromCategory),
		ToID: c.ToID, ToName: c.ToName, ToCategory: string(c.ToCategory),
	}
}

func fromStatusChangeData(d statusChangeData) *model.StatusChange {
	return &model.StatusChange{
		FromID: d.FromID, FromName: d.FromName, FromCategory: model.IssueState(d.FromCategory),
		ToID: d.ToID, ToName: d.ToName, ToCategory: model.IssueState(d.ToCategory),
	}
}

func labelRefs(labels []*model.Label) []labelRef {
	out := make([]labelRef, len(labels))
	for i, l := range labels {
		out[i] = labelRef{ID: l.ID, Name: l.Name}
	}
	return out
}

// fromEventRow arma el evento del dominio. Un JSONB que no decodifica es un
// problema de datos: error interno.
func fromEventRow(r *eventRow) (*model.IssueEvent, error) {
	ev := &model.IssueEvent{
		ID:        r.ID,
		IssueID:   r.IssueID,
		ActorUID:  r.ActorUID,
		Kind:      model.EventKind(r.Kind),
		CreatedAt: r.CreatedAt,
	}
	var err error
	switch ev.Kind {
	case model.EventStatusChanged, model.EventRestored:
		var d statusChangeData
		err = json.Unmarshal(r.Data, &d)
		ev.Status = fromStatusChangeData(d)
	case model.EventDiscarded:
		var d discardedData
		err = json.Unmarshal(r.Data, &d)
		ev.Status, ev.Reason = fromStatusChangeData(d.statusChangeData), d.Reason
	case model.EventTitleChanged:
		var d titleChangeData
		err = json.Unmarshal(r.Data, &d)
		ev.Title = &model.TitleChange{From: d.From, To: d.To}
	case model.EventPriorityChanged:
		var d priorityChangeData
		err = json.Unmarshal(r.Data, &d)
		ev.Priority = &model.PriorityChange{From: model.Priority(d.From), To: model.Priority(d.To)}
	case model.EventLabelsChanged:
		var d labelsChangeData
		err = json.Unmarshal(r.Data, &d)
		ev.Labels = &model.LabelsChange{Added: refsToLabels(d.Added), Removed: refsToLabels(d.Removed)}
	case model.EventCommented:
		if r.CommentID == nil || r.CommentBody == nil || r.CommentAuthorUID == nil || r.CommentCreatedAt == nil {
			return nil, fmt.Errorf("event %s: comment missing", r.ID)
		}
		ev.Comment = &model.Comment{
			ID: *r.CommentID, IssueID: r.IssueID, AuthorUID: *r.CommentAuthorUID,
			Body: *r.CommentBody, CreatedAt: *r.CommentCreatedAt,
		}
	}
	if err != nil {
		return nil, fmt.Errorf("event %s: decode %s: %w", r.ID, r.Kind, err)
	}
	return ev, nil
}

func refsToLabels(refs []labelRef) []*model.Label {
	out := make([]*model.Label, len(refs))
	for i, r := range refs {
		out[i] = &model.Label{ID: r.ID, Name: r.Name}
	}
	return out
}

// getIssue lee un issue con sus etiquetas (gorm.ErrRecordNotFound si no está).
func getIssue(db *gorm.DB, projectID string, number int32) (*model.Issue, error) {
	var row issueRow
	if err := db.Where("project_id = ? AND number = ?", projectID, number).First(&row).Error; err != nil {
		return nil, err
	}
	issue := fromIssueRow(&row)
	if err := attachLabels(db, []*model.Issue{issue}); err != nil {
		return nil, err
	}
	return issue, nil
}

// lockIssue lee el issue con FOR UPDATE (serializa los cambios del mismo
// issue).
func lockIssue(tx *gorm.DB, projectID string, number int32) (*model.Issue, error) {
	var rows []issueRow
	err := tx.Raw(`SELECT * FROM issues.issues WHERE project_id = ? AND number = ? FOR UPDATE`, projectID, number).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	issue := fromIssueRow(&rows[0])
	if err := attachLabels(tx, []*model.Issue{issue}); err != nil {
		return nil, err
	}
	return issue, nil
}

// attachLabels llena LabelIDs de cada issue, ordenadas por nombre de etiqueta
// (sin etiquetas: lista vacía, no nil).
func attachLabels(db *gorm.DB, issues []*model.Issue) error {
	if len(issues) == 0 {
		return nil
	}
	byID := make(map[string]*model.Issue, len(issues))
	ids := make([]string, len(issues))
	for i, it := range issues {
		it.LabelIDs = []string{}
		byID[it.ID] = it
		ids[i] = it.ID
	}
	var pairs []struct {
		IssueID string
		LabelID string
	}
	err := db.Raw(
		`SELECT il.issue_id, il.label_id
		   FROM issues.issue_labels il
		   JOIN issues.labels l ON l.id = il.label_id
		  WHERE il.issue_id IN ?
		  ORDER BY lower(l.name), l.id`, ids,
	).Scan(&pairs).Error
	if err != nil {
		return err
	}
	for _, p := range pairs {
		byID[p.IssueID].LabelIDs = append(byID[p.IssueID].LabelIDs, p.LabelID)
	}
	return nil
}

// Move pasa el issue a target y, si place no es cero, lo reubica entre sus
// vecinos. Toma el lock del proyecto (los ranks se calculan contra todo el
// orden del proyecto) y el del issue.
func (r *IssueRepository) Move(ctx context.Context, projectID string, number int32, target *model.Status, place port.Placement, actorUID string) (*model.Issue, error) {
	details := map[string]any{"project_id": projectID, "number": number}
	var out *model.Issue
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProject(tx, projectID); err != nil {
			return err
		}
		cur, err := lockIssue(tx, projectID, number)
		if err != nil {
			return err
		}
		// El service eligió el destino antes de la transacción: se relee con el
		// lock del proyecto tomado (pudo borrarse o renombrarse).
		targetRow, err := getStatusRow(tx, projectID, target.ID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return statusGone(target.ID)
		}
		if err != nil {
			return err
		}
		target = fromStatusRow(targetRow)
		statusChanged := cur.StatusID != target.ID
		if !statusChanged && place.IsZero() {
			out = cur
			return nil
		}
		rank := cur.Rank
		if !place.IsZero() {
			if rank, err = placeRank(tx, projectID, cur, target.ID, place); err != nil {
				return err
			}
		}
		now := dbNow()
		updates := map[string]any{"status_id": target.ID, "state": string(target.Category), "rank": rank, "updated_at": now}
		if target.Category != model.IssueStateCanceled {
			// Sale de CANCELED (o nunca estuvo): sin motivo de descarte.
			clearDiscard(updates)
			cur.Discard = nil
		}
		if err := tx.Model(&issueRow{}).Where("id = ?", cur.ID).Updates(updates).Error; err != nil {
			return err
		}
		if statusChanged {
			from, err := getStatusRow(tx, projectID, cur.StatusID)
			if err != nil {
				return err
			}
			ev := &model.IssueEvent{Kind: model.EventStatusChanged, Status: model.NewStatusChange(fromStatusRow(from), target)}
			if err := insertEvent(tx, cur.ID, actorUID, ev, now); err != nil {
				return err
			}
		}
		cur.StatusID, cur.State, cur.Rank, cur.UpdatedAt = target.ID, target.Category, rank, now
		out = cur
		return nil
	})
	if err != nil {
		return nil, writeError(err, "issue", "move", details)
	}
	return out, nil
}

// placeRank calcula un rank para cur entre sus vecinos de la columna
// statusID. El orden de rank es de todo el proyecto, así que el rank nuevo
// sale entre el vecino y la clave siguiente (o anterior) del proyecto: nunca
// choca con un issue de otra columna.
func placeRank(tx *gorm.DB, projectID string, cur *model.Issue, statusID string, place port.Placement) (string, error) {
	stale := func(msg string) error {
		return domainerrors.FailedPreconditionError(msg, map[string]any{
			"after_number": place.AfterNumber, "before_number": place.BeforeNumber,
		}).WithReason("STALE_ORDER")
	}
	neighbor := func(n int32) (string, error) {
		var rows []issueRow
		if err := tx.Where("project_id = ? AND number = ?", projectID, n).Limit(1).Find(&rows).Error; err != nil {
			return "", err
		}
		if len(rows) == 0 || rows[0].StatusID != statusID {
			return "", stale("neighbor is not in the target column")
		}
		return rows[0].Rank, nil
	}
	lo, hi := "", ""
	var err error
	if place.AfterNumber != 0 {
		if lo, err = neighbor(place.AfterNumber); err != nil {
			return "", err
		}
	}
	if place.BeforeNumber != 0 {
		if hi, err = neighbor(place.BeforeNumber); err != nil {
			return "", err
		}
	}
	if lo != "" && hi != "" && lo >= hi {
		return "", stale("neighbors are out of order")
	}
	// Los vecinos tienen que ser contiguos en la columna, tal como la ve el
	// cliente: un issue de la columna entre ellos (o más allá del lado sin
	// vecino) quiere decir que la vista está vieja.
	between := tx.Model(&issueRow{}).Where("project_id = ? AND status_id = ? AND id <> ?", projectID, statusID, cur.ID)
	if lo != "" {
		between = between.Where("rank > ?", lo)
	}
	if hi != "" {
		between = between.Where("rank < ?", hi)
	}
	var inBetween int64
	if err := between.Count(&inBetween).Error; err != nil {
		return "", err
	}
	if inBetween > 0 {
		return "", stale("neighbors are not adjacent in the target column")
	}
	// Ajusta el lado abierto a la clave contigua del proyecto (sin cur).
	var keys []string
	if lo != "" {
		err = tx.Model(&issueRow{}).Where("project_id = ? AND rank > ? AND id <> ?", projectID, lo, cur.ID).
			Order("rank ASC").Limit(1).Pluck("rank", &keys).Error
		if err == nil {
			hi = firstOr(keys, "")
		}
	} else {
		err = tx.Model(&issueRow{}).Where("project_id = ? AND rank < ? AND id <> ?", projectID, hi, cur.ID).
			Order("rank DESC").Limit(1).Pluck("rank", &keys).Error
		if err == nil {
			lo = firstOr(keys, "")
		}
	}
	if err != nil {
		return "", err
	}
	rank, err := model.RankBetween(lo, hi)
	if err != nil {
		// Ranks guardados mal formados: problema de datos, no del cliente.
		return "", fmt.Errorf("rank between %q and %q: %v", lo, hi, err)
	}
	return rank, nil
}

func firstOr(keys []string, def string) string {
	if len(keys) > 0 {
		return keys[0]
	}
	return def
}

// Update aplica el patch y registra un evento por cada cambio real.
func (r *IssueRepository) Update(ctx context.Context, projectID string, number int32, patch model.IssuePatch, actorUID string) (*model.Issue, error) {
	details := map[string]any{"project_id": projectID, "number": number}
	var out *model.Issue
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cur, err := lockIssue(tx, projectID, number)
		if err != nil {
			return err
		}
		labels := map[string]*model.Label{}
		if patch.LabelIDs != nil {
			var rows []labelRow
			if err := tx.Where("project_id = ?", projectID).Find(&rows).Error; err != nil {
				return err
			}
			for i := range rows {
				labels[rows[i].ID] = fromLabelRow(&rows[i])
			}
		}
		events := model.IssueChanges(cur, patch, labels)
		if len(events) == 0 {
			out = cur
			return nil
		}

		now := dbNow()
		updates := map[string]any{"updated_at": now}
		for _, ev := range events {
			switch ev.Kind {
			case model.EventTitleChanged:
				updates["title"] = ev.Title.To
			case model.EventDescriptionChanged:
				updates["description"] = *patch.Description
			case model.EventPriorityChanged:
				updates["priority"] = int(ev.Priority.To)
			case model.EventLabelsChanged:
				if err := replaceLabels(tx, cur.ID, ev.Labels); err != nil {
					return err
				}
			}
		}
		if err := tx.Model(&issueRow{}).Where("id = ?", cur.ID).Updates(updates).Error; err != nil {
			return err
		}
		for _, ev := range events {
			if err := insertEvent(tx, cur.ID, actorUID, ev, now); err != nil {
				return err
			}
		}
		out, err = getIssue(tx, projectID, number)
		return err
	})
	if err != nil {
		return nil, writeError(err, "issue", "update", details)
	}
	return out, nil
}

func replaceLabels(tx *gorm.DB, issueID string, change *model.LabelsChange) error {
	if len(change.Removed) > 0 {
		ids := make([]string, len(change.Removed))
		for i, l := range change.Removed {
			ids[i] = l.ID
		}
		if err := tx.Exec(`DELETE FROM issues.issue_labels WHERE issue_id = ? AND label_id IN ?`, issueID, ids).Error; err != nil {
			return err
		}
	}
	for _, l := range change.Added {
		if err := tx.Exec(`INSERT INTO issues.issue_labels (issue_id, label_id) VALUES (?, ?)`, issueID, l.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// AddComment guarda el comentario y su evento.
func (r *IssueRepository) AddComment(ctx context.Context, projectID string, number int32, comment *model.Comment) (*model.Comment, error) {
	details := map[string]any{"project_id": projectID, "number": number}
	now := dbNow()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// FOR UPDATE: los eventos de un issue se escriben de a uno, así el seq
		// de cada evento sigue el orden de commit y el cursor de ListEvents no
		// salta ninguno.
		var ids []string
		err := tx.Raw(`SELECT id FROM issues.issues WHERE project_id = ? AND number = ? FOR UPDATE`, projectID, number).
			Scan(&ids).Error
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return gorm.ErrRecordNotFound
		}
		comment.IssueID, comment.CreatedAt = ids[0], now
		row := &commentRow{ID: comment.ID, IssueID: comment.IssueID, AuthorUID: comment.AuthorUID, Body: comment.Body, CreatedAt: now}
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		return insertEvent(tx, comment.IssueID, comment.AuthorUID, &model.IssueEvent{Kind: model.EventCommented, Comment: comment}, now)
	})
	if err != nil {
		return nil, writeError(err, "issue", "comment", details)
	}
	return comment, nil
}

// ListEvents pagina el historial del issue por seq ASC.
func (r *IssueRepository) ListEvents(ctx context.Context, projectID string, number int32, page port.PageFilter) (*port.EventPage, error) {
	details := map[string]any{"project_id": projectID, "number": number}
	db := r.db.WithContext(ctx)
	var ids []string
	if err := db.Model(&issueRow{}).Where("project_id = ? AND number = ?", projectID, number).Pluck("id", &ids).Error; err != nil {
		return nil, translateError(err, "issue", "get", details)
	}
	if len(ids) == 0 {
		return nil, domainerrors.NotFoundError("issue not found", details)
	}
	after := int64(0)
	if page.Cursor != "" {
		var c eventCursor
		if err := decodeCursor(page.Cursor, &c); err != nil || c.Seq <= 0 {
			return nil, domainerrors.ValidationError("invalid cursor", map[string]any{"field": "cursor"})
		}
		after = c.Seq
	}
	limit := int(page.Limit)
	var rows []eventRow
	err := db.Raw(
		`SELECT e.id, e.seq, e.issue_id, e.actor_uid, e.kind, e.data, e.created_at,
		        c.id AS comment_id, c.author_uid AS comment_author_uid, c.body AS comment_body,
		        c.created_at AS comment_created_at
		   FROM issues.issue_events e
		   LEFT JOIN issues.comments c ON c.id = e.comment_id
		  WHERE e.issue_id = ? AND e.seq > ?
		  ORDER BY e.seq ASC
		  LIMIT ?`, ids[0], after, limit+1,
	).Scan(&rows).Error
	if err != nil {
		return nil, translateError(err, "issue events", "list", details)
	}
	hasNext := len(rows) > limit
	if hasNext {
		rows = rows[:limit]
	}
	out := &port.EventPage{Events: make([]*model.IssueEvent, len(rows)), HasNext: hasNext}
	for i := range rows {
		ev, err := fromEventRow(&rows[i])
		if err != nil {
			return nil, domainerrors.InternalError("failed to read issue events", map[string]any{"cause": err.Error()})
		}
		out.Events[i] = ev
	}
	if hasNext && len(rows) > 0 {
		out.NextCursor = encodeCursor(eventCursor{Seq: rows[len(rows)-1].Seq})
	}
	return out, nil
}

// statusGone: el estado elegido se borró antes de escribir (otra request).
func statusGone(id string) error {
	return domainerrors.FailedPreconditionError("status no longer exists", map[string]any{"status_id": id}).
		WithReason("STALE_ORDER")
}

// writeError: los DomainError (STALE_ORDER, NotFound del proyecto) pasan tal
// cual; el resto se traduce.
func writeError(err error, entity, op string, details map[string]any) error {
	var de *domainerrors.DomainError
	if errors.As(err, &de) {
		return de
	}
	return translateError(err, entity, op, details)
}

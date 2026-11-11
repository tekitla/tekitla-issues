package postgres

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Historial importado (issue 036): ImportEvents de IssueRepository y la
// corrección del alta de ImportIssue (override_created_at).

// ImportEvents agrega los eventos al historial del issue de sourceKey, con
// el lock del issue. La idempotencia es el índice único (issue_id,
// source_ref) de 000006: las referencias que ya están se omiten o, con
// overwrite, se reescriben.
func (r *IssueRepository) ImportEvents(ctx context.Context, workspaceIDs []string, projectID, sourceKey string, events []model.ImportedEvent, overwrite bool, author model.Author) (model.ImportEventsResult, error) {
	details := map[string]any{"project_id": projectID, "source_key": sourceKey}
	var res model.ImportEventsResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []string
		err := tx.Raw(`SELECT id FROM issues.projects WHERE id = ? AND workspace_id IN ?`, projectID, workspaceIDs).Scan(&ids).Error
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return domainerrors.NotFoundError("project not found", map[string]any{"id": projectID})
		}
		var rows []issueRow
		err = tx.Raw(`SELECT * FROM issues.issues WHERE project_id = ? AND source_key = ? FOR UPDATE`, projectID, sourceKey).Scan(&rows).Error
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return domainerrors.NotFoundError("issue not found", details)
		}
		issue := fromIssueRow(&rows[0])
		refs := make([]string, len(events))
		for i, ev := range events {
			if ev.OccurredAt.Before(issue.CreatedAt) {
				return domainerrors.ValidationError("occurred_at is before the issue's created_at", map[string]any{
					"field": fmt.Sprintf("events[%d].occurred_at", i), "created_at": issue.CreatedAt.Format(time.RFC3339),
				})
			}
			refs[i] = ev.SourceRef
		}
		var existing []string
		err = tx.Raw(`SELECT source_ref FROM issues.issue_events WHERE issue_id = ? AND source_ref IN ?`, issue.ID, refs).Scan(&existing).Error
		if err != nil {
			return err
		}
		have := make(map[string]bool, len(existing))
		for _, ref := range existing {
			have[ref] = true
		}
		for _, ev := range events {
			at := ev.OccurredAt.UTC().Truncate(time.Microsecond)
			e := &model.IssueEvent{Kind: model.EventStatusChanged, Status: ev.Status, Source: ev.Source, SourceRef: ev.SourceRef}
			switch {
			case !have[ev.SourceRef]:
				if err := insertEvent(tx, issue.ID, author, e, at); err != nil {
					return err
				}
				res.Created++
			case overwrite:
				data, err := eventData(e)
				if err != nil {
					return err
				}
				err = tx.Exec(`UPDATE issues.issue_events SET created_at = ?, data = ?::jsonb, source = ?
				                WHERE issue_id = ? AND source_ref = ?`,
					at, string(data), string(ev.Source), issue.ID, ev.SourceRef).Error
				if err != nil {
					return err
				}
				res.Updated++
			default:
				res.Skipped++
			}
		}
		return nil
	})
	if err != nil {
		return model.ImportEventsResult{}, writeError(err, "issue events", "import", details)
	}
	return res, nil
}

// overrideCreatedAt corrige el alta de un issue importado y la fecha de su
// evento «creado». Devuelve si cambió. No puede quedar después de un evento
// importado con ImportEvents (el historial no empezaría por el alta).
func overrideCreatedAt(tx *gorm.DB, cur *model.Issue, createdAt time.Time, details map[string]any) (bool, error) {
	at := createdAt.UTC().Truncate(time.Microsecond)
	if at.Equal(cur.CreatedAt) {
		return false, nil
	}
	var first []time.Time
	err := tx.Raw(`SELECT min(created_at) FROM issues.issue_events
	                WHERE issue_id = ? AND source IN ('import_history', 'import_file')
	               HAVING count(*) > 0`, cur.ID).Scan(&first).Error
	if err != nil {
		return false, err
	}
	if len(first) > 0 && at.After(first[0]) {
		d := cloneDetails(details)
		d["first_imported_event_at"] = first[0].Format(time.RFC3339)
		return false, domainerrors.FailedPreconditionError("created_at would be after an imported event", d).WithReason("IMPORT_EVENTS_BEFORE_CREATED")
	}
	if err := tx.Model(&issueRow{}).Where("id = ?", cur.ID).Update("created_at", at).Error; err != nil {
		return false, err
	}
	if err := tx.Exec(`UPDATE issues.issue_events SET created_at = ? WHERE issue_id = ? AND kind = 'created'`, at, cur.ID).Error; err != nil {
		return false, err
	}
	return true, nil
}

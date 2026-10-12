package postgres

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Escrituras de IssueRepository del issue 011: descartar con motivo y
// restaurar. Mismo patrón que Move: lock del proyecto (el destino se relee
// con el lock tomado) y del issue, y el evento en la misma transacción.

// Discard pasa el issue a target (CANCELED) conservando su rank y guarda el
// motivo.
func (r *IssueRepository) Discard(ctx context.Context, projectID string, number int32, statusID, reason, actorUID string) (*model.Issue, error) {
	details := map[string]any{"project_id": projectID, "number": number}
	var out *model.Issue
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cur, from, to, err := lockForStatusChange(tx, projectID, number, statusID, model.IssueStateCanceled)
		if err != nil {
			return err
		}
		if cur.State == model.IssueStateCanceled {
			return domainerrors.FailedPreconditionError("issue is already discarded", details).WithReason("ISSUE_ALREADY_DISCARDED")
		}
		now := dbNow()
		if err := tx.Model(&issueRow{}).Where("id = ?", cur.ID).Updates(map[string]any{
			"status_id": to.ID, "state": string(to.Category), "updated_at": now,
			"discard_reason": reason, "discarded_by": actorUID, "discarded_at": now,
		}).Error; err != nil {
			return err
		}
		ev := &model.IssueEvent{Kind: model.EventDiscarded, Status: model.NewStatusChange(from, to), Reason: reason}
		if err := insertEvent(tx, cur.ID, actorUID, ev, now); err != nil {
			return err
		}
		cur.StatusID, cur.State, cur.UpdatedAt = to.ID, to.Category, now
		cur.Discard = &model.DiscardInfo{Reason: reason, ActorUID: actorUID, DiscardedAt: now}
		out = cur
		return nil
	})
	if err != nil {
		return nil, writeError(err, "issue", "discard", details)
	}
	return out, nil
}

// Restore pasa el issue a target (TODO), al final de esa columna, y borra el
// motivo del descarte.
func (r *IssueRepository) Restore(ctx context.Context, projectID string, number int32, actorUID string) (*model.Issue, error) {
	details := map[string]any{"project_id": projectID, "number": number}
	var out *model.Issue
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cur, from, to, err := lockForStatusChange(tx, projectID, number, "", model.IssueStateTodo)
		if err != nil {
			return err
		}
		if cur.State != model.IssueStateCanceled {
			return domainerrors.FailedPreconditionError("issue is not discarded", details).WithReason("ISSUE_NOT_DISCARDED")
		}
		rank, err := rankAtColumnEnd(tx, projectID, cur, to.ID)
		if err != nil {
			return err
		}
		now := dbNow()
		updates := map[string]any{"status_id": to.ID, "state": string(to.Category), "rank": rank, "updated_at": now}
		clearDiscard(updates)
		if err := tx.Model(&issueRow{}).Where("id = ?", cur.ID).Updates(updates).Error; err != nil {
			return err
		}
		ev := &model.IssueEvent{Kind: model.EventRestored, Status: model.NewStatusChange(from, to)}
		if err := insertEvent(tx, cur.ID, actorUID, ev, now); err != nil {
			return err
		}
		cur.StatusID, cur.State, cur.Rank, cur.UpdatedAt, cur.Discard = to.ID, to.Category, rank, now, nil
		out = cur
		return nil
	})
	if err != nil {
		return nil, writeError(err, "issue", "restore", details)
	}
	return out, nil
}

// lockForStatusChange toma el lock del proyecto y del issue, y lee el estado
// actual del issue y el destino con el lock tomado: targetID, que tiene que
// ser de category (pudo borrarse: STALE_ORDER), o "" = el primero de
// category por rank.
func lockForStatusChange(tx *gorm.DB, projectID string, number int32, targetID string, category model.IssueState) (cur *model.Issue, from, to *model.Status, err error) {
	if err = lockProject(tx, projectID); err != nil {
		return nil, nil, nil, err
	}
	if cur, err = lockIssue(tx, projectID, number); err != nil {
		return nil, nil, nil, err
	}
	var toRow *statusRow
	if targetID == "" {
		rows, err := listStatusRows(tx, projectID)
		if err != nil {
			return nil, nil, nil, err
		}
		for i := range rows {
			if model.IssueState(rows[i].Category) == category {
				toRow = &rows[i]
				break
			}
		}
		if toRow == nil {
			// Ninguna escritura deja una categoría vacía: problema de datos.
			return nil, nil, nil, fmt.Errorf("project %s has no %s status", projectID, category)
		}
	} else {
		toRow, err = getStatusRow(tx, projectID, targetID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, statusGone(targetID)
		}
		if err != nil {
			return nil, nil, nil, err
		}
		if model.IssueState(toRow.Category) != category {
			// El service validó la categoría y la categoría no cambia.
			return nil, nil, nil, fmt.Errorf("status %s is %s, want %s", toRow.ID, toRow.Category, category)
		}
	}
	fromRow, err := getStatusRow(tx, projectID, cur.StatusID)
	if err != nil {
		return nil, nil, nil, err
	}
	return cur, fromStatusRow(fromRow), fromStatusRow(toRow), nil
}

// rankAtColumnEnd devuelve un rank para cur al final de la columna statusID:
// entre el último issue de la columna y la clave siguiente del proyecto (el
// orden es de todo el proyecto). Columna vacía: conserva su rank.
func rankAtColumnEnd(tx *gorm.DB, projectID string, cur *model.Issue, statusID string) (string, error) {
	var keys []string
	err := tx.Model(&issueRow{}).Where("project_id = ? AND status_id = ? AND id <> ?", projectID, statusID, cur.ID).
		Order("rank DESC").Limit(1).Pluck("rank", &keys).Error
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return cur.Rank, nil
	}
	lo := keys[0]
	keys = nil
	err = tx.Model(&issueRow{}).Where("project_id = ? AND rank > ? AND id <> ?", projectID, lo, cur.ID).
		Order("rank ASC").Limit(1).Pluck("rank", &keys).Error
	if err != nil {
		return "", err
	}
	hi := firstOr(keys, "")
	rank, err := model.RankBetween(lo, hi)
	if err != nil {
		// Ranks guardados mal formados: problema de datos, no del cliente.
		return "", fmt.Errorf("rank between %q and %q: %v", lo, hi, err)
	}
	return rank, nil
}

// clearDiscard agrega a updates el borrado del motivo de descarte (el issue
// sale de CANCELED).
func clearDiscard(updates map[string]any) {
	updates["discard_reason"] = nil
	updates["discarded_by"] = nil
	updates["discarded_at"] = nil
}

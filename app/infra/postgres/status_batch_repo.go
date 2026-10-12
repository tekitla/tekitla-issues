package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// BatchUpdate guarda la lista completa de estados (BatchUpdateStatuses) en
// una transacción con el lock del proyecto. Pasos, en este orden para no
// chocar con los índices únicos de nombre y rank a mitad del cambio:
//
//  1. Valida con model.PlanStatusBatch contra los estados actuales.
//  2. Mueve los issues de cada estado borrado a su destino (con su evento,
//     con el nombre final del destino) y borra el estado.
//  3. Pone nombre y rank provisorios (derivados del id) a los que quedan.
//  4. Guarda cada estado con su nombre final y un rank nuevo según su
//     posición (a0, a1, ...), y crea los nuevos.
func (r *StatusRepository) BatchUpdate(ctx context.Context, projectID string, specs []model.StatusSpec, deletions []model.StatusDeletion, actorUID string) ([]*model.Status, error) {
	var out []statusRow
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProject(tx, projectID); err != nil {
			return err
		}
		rows, err := listStatusRows(tx, projectID)
		if err != nil {
			return err
		}
		current := make([]*model.Status, len(rows))
		rowByID := make(map[string]*statusRow, len(rows))
		for i := range rows {
			current[i] = fromStatusRow(&rows[i])
			rowByID[rows[i].ID] = &rows[i]
		}
		plan, err := model.PlanStatusBatch(current, specs, deletions, uuid.NewString)
		if err != nil {
			return err
		}
		finalByID := make(map[string]*model.Status, len(plan.Final))
		rank := ""
		for _, st := range plan.Final {
			if rank, err = model.RankAfter(rank); err != nil {
				return fmt.Errorf("rank after %q: %v", rank, err)
			}
			st.ProjectID, st.Rank = projectID, rank
			finalByID[st.ID] = st
		}

		for _, d := range plan.Deletions {
			var count int64
			if err := tx.Model(&issueRow{}).Where("project_id = ? AND status_id = ?", projectID, d.StatusID).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				if d.MoveToStatusID == "" {
					return domainerrors.FailedPreconditionError("status has issues: move_to_status_id is required",
						map[string]any{"status_id": d.StatusID, "issues": count}).WithReason("STATUS_HAS_ISSUES")
				}
				if err := moveAllIssues(tx, projectID, rowByID[d.StatusID], toStatusRow(finalByID[d.MoveToStatusID]), actorUID); err != nil {
					return err
				}
			}
			if err := tx.Where("project_id = ? AND id = ?", projectID, d.StatusID).Delete(&statusRow{}).Error; err != nil {
				return err
			}
		}

		var keptIDs []string
		for _, st := range plan.Final {
			if !plan.IsNew[st.ID] {
				keptIDs = append(keptIDs, st.ID)
			}
		}
		if len(keptIDs) > 0 {
			// Provisorios únicos: el id no choca con ningún nombre ni rank final.
			if err := tx.Exec(`UPDATE issues.statuses SET name = 'tmp:' || id::text, rank = 'tmp:' || id::text
			                    WHERE project_id = ? AND id IN ?`, projectID, keptIDs).Error; err != nil {
				return err
			}
		}

		now := dbNow()
		for _, st := range plan.Final {
			row := toStatusRow(st)
			row.UpdatedAt = now
			if plan.IsNew[st.ID] {
				row.CreatedAt = now
				if err := tx.Create(row).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Model(&statusRow{}).Where("project_id = ? AND id = ?", projectID, st.ID).Updates(map[string]any{
				"name": row.Name, "rank": row.Rank, "color": row.Color, "wip_limit": row.WIPLimit,
				"hidden": row.Hidden, "updated_at": now,
			}).Error; err != nil {
				return err
			}
		}
		out, err = listStatusRows(tx, projectID)
		return err
	})
	if err != nil {
		return nil, statusError(err, "batch update", map[string]any{"project_id": projectID})
	}
	statuses := make([]*model.Status, len(out))
	for i := range out {
		statuses[i] = fromStatusRow(&out[i])
	}
	return statuses, nil
}

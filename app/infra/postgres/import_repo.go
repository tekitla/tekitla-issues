package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Import de IssueRepository (issue 025): crea o actualiza un issue importado
// de otro tracker por su clave de origen, con el lock del proyecto (como
// Create: el número y el rank se deciden con la fila del proyecto tomada).

// Import crea o actualiza el issue de spec.SourceKey.
func (r *IssueRepository) Import(ctx context.Context, workspaceIDs []string, spec model.ImportSpec, author model.Author) (*model.Issue, model.ImportOutcome, error) {
	details := map[string]any{"project_id": spec.ProjectID, "source_key": spec.SourceKey, "number": spec.Number}
	var (
		out     *model.Issue
		outcome model.ImportOutcome
	)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []string
		err := tx.Raw(`SELECT id FROM issues.projects WHERE id = ? AND workspace_id IN ? FOR UPDATE`, spec.ProjectID, workspaceIDs).
			Scan(&ids).Error
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return domainerrors.NotFoundError("project not found", map[string]any{"id": spec.ProjectID})
		}
		statusRows, err := listStatusRows(tx, spec.ProjectID)
		if err != nil {
			return err
		}
		statuses := make([]*model.Status, len(statusRows))
		for i := range statusRows {
			statuses[i] = fromStatusRow(&statusRows[i])
		}

		var existing []issueRow
		err = tx.Raw(`SELECT * FROM issues.issues WHERE project_id = ? AND source_key = ? FOR UPDATE`, spec.ProjectID, spec.SourceKey).
			Scan(&existing).Error
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			if _, err := importCreate(tx, spec, statuses, author, details); err != nil {
				return err
			}
			outcome = model.ImportCreated
			// Se relee: así la respuesta trae las etiquetas que se pusieron.
			out, err = getIssue(tx, spec.ProjectID, spec.Number)
			return err
		}
		cur := fromIssueRow(&existing[0])
		if cur.Number != spec.Number {
			d := cloneDetails(details)
			d["stored_number"] = cur.Number
			return domainerrors.FailedPreconditionError("number does not match the imported issue", d).WithReason("IMPORT_NUMBER_MISMATCH")
		}
		changed, err := importUpdate(tx, cur, spec, statuses, author)
		if err != nil {
			return err
		}
		outcome = model.ImportUnchanged
		if changed {
			outcome = model.ImportUpdated
		}
		out, err = getIssue(tx, spec.ProjectID, spec.Number)
		return err
	})
	if err != nil {
		return nil, "", writeError(err, "issue", "import", details)
	}
	return out, outcome, nil
}

// importCreate inserta el issue con su número, al final del orden, en el
// primer estado de su categoría, con sus etiquetas y el evento «creado».
func importCreate(tx *gorm.DB, spec model.ImportSpec, statuses []*model.Status, author model.Author, details map[string]any) (*model.Issue, error) {
	var taken int64
	if err := tx.Model(&issueRow{}).Where("project_id = ? AND number = ?", spec.ProjectID, spec.Number).Count(&taken).Error; err != nil {
		return nil, err
	}
	if taken > 0 {
		return nil, domainerrors.ConflictError("number is already used by another issue", details).WithReason("ISSUE_NUMBER_TAKEN")
	}
	target := model.FirstStatusOf(statuses, spec.State)
	if target == nil {
		return nil, fmt.Errorf("project has no %s status", spec.State)
	}
	var last []string
	if err := tx.Model(&issueRow{}).Where("project_id = ?", spec.ProjectID).Order("rank DESC").Limit(1).Pluck("rank", &last).Error; err != nil {
		return nil, err
	}
	rank, err := model.RankAfter(firstOr(last, ""))
	if err != nil {
		return nil, fmt.Errorf("rank after %q: %v", firstOr(last, ""), err)
	}
	now := dbNow()
	createdAt := spec.CreatedAt.UTC().Truncate(time.Microsecond)
	row := &issueRow{
		ID:          uuid.NewString(),
		ProjectID:   spec.ProjectID,
		Number:      spec.Number,
		Title:       spec.Title,
		Description: spec.Description,
		State:       string(target.Category),
		Priority:    int(spec.Priority),
		Rank:        rank,
		StatusID:    target.ID,
		SourceKey:   &spec.SourceKey,
		CreatedAt:   createdAt,
		UpdatedAt:   now,
	}
	if err := tx.Create(row).Error; err != nil {
		return nil, err
	}
	// El siguiente CreateIssue usa un número mayor que todos, importados
	// incluidos.
	err = tx.Exec(`UPDATE issues.projects SET next_issue_number = GREATEST(next_issue_number, ?) WHERE id = ?`,
		spec.Number+1, spec.ProjectID).Error
	if err != nil {
		return nil, err
	}
	if err := insertEvent(tx, row.ID, author, &model.IssueEvent{Kind: model.EventCreated}, now); err != nil {
		return nil, err
	}
	issue := fromIssueRow(row)
	issue.LabelIDs = []string{}
	if _, err := syncImportedLabels(tx, issue, spec.LabelNames, author, now); err != nil {
		return nil, err
	}
	return issue, nil
}

// importUpdate pisa título, descripción, prioridad, categoría y etiquetas
// del importador. Devuelve si cambió algo.
func importUpdate(tx *gorm.DB, cur *model.Issue, spec model.ImportSpec, statuses []*model.Status, author model.Author) (bool, error) {
	if err := attachLabels(tx, []*model.Issue{cur}); err != nil {
		return false, err
	}
	now := dbNow()
	patch := model.IssuePatch{Title: &spec.Title, Description: &spec.Description, Priority: &spec.Priority}
	events := model.IssueChanges(cur, patch, nil)
	updates := map[string]any{}
	for _, ev := range events {
		switch ev.Kind {
		case model.EventTitleChanged:
			updates["title"] = spec.Title
		case model.EventDescriptionChanged:
			updates["description"] = spec.Description
		case model.EventPriorityChanged:
			updates["priority"] = int(spec.Priority)
		}
	}
	if cur.State != spec.State {
		target := model.FirstStatusOf(statuses, spec.State)
		from := statusByID(statuses, cur.StatusID)
		if target == nil || from == nil {
			return false, fmt.Errorf("project statuses are inconsistent")
		}
		updates["status_id"], updates["state"] = target.ID, string(target.Category)
		if target.Category != model.IssueStateCanceled {
			clearDiscard(updates)
		}
		events = append(events, &model.IssueEvent{Kind: model.EventStatusChanged, Status: model.NewStatusChange(from, target)})
	}
	if len(updates) > 0 {
		updates["updated_at"] = now
		if err := tx.Model(&issueRow{}).Where("id = ?", cur.ID).Updates(updates).Error; err != nil {
			return false, err
		}
	}
	for _, ev := range events {
		if err := insertEvent(tx, cur.ID, author, ev, now); err != nil {
			return false, err
		}
	}
	labelsChanged, err := syncImportedLabels(tx, cur, spec.LabelNames, author, now)
	if err != nil {
		return false, err
	}
	if labelsChanged && len(updates) == 0 {
		if err := tx.Model(&issueRow{}).Where("id = ?", cur.ID).Update("updated_at", now).Error; err != nil {
			return false, err
		}
	}
	return len(events) > 0 || labelsChanged, nil
}

// syncImportedLabels deja en el issue las etiquetas names como etiquetas
// del importador: agrega las que faltan (creándolas en el proyecto con color
// neutral si no existen) y quita las del importador que ya no vienen. Las
// puestas a mano no se tocan, y una que ya está puesta a mano queda así.
// Registra un evento labels_changed si cambió algo.
func syncImportedLabels(tx *gorm.DB, issue *model.Issue, names []string, author model.Author, now time.Time) (bool, error) {
	var current []struct {
		LabelID  string
		Imported bool
	}
	if err := tx.Raw(`SELECT label_id, imported FROM issues.issue_labels WHERE issue_id = ?`, issue.ID).Scan(&current).Error; err != nil {
		return false, err
	}
	manual, imported := map[string]bool{}, map[string]bool{}
	for _, c := range current {
		if c.Imported {
			imported[c.LabelID] = true
		} else {
			manual[c.LabelID] = true
		}
	}
	wanted, err := ensureLabels(tx, issue.ProjectID, names, now)
	if err != nil {
		return false, err
	}
	change := &model.LabelsChange{}
	wantedIDs := map[string]bool{}
	for _, l := range wanted {
		wantedIDs[l.ID] = true
		if !manual[l.ID] && !imported[l.ID] {
			change.Added = append(change.Added, l)
		}
	}
	for id := range imported {
		if !wantedIDs[id] {
			change.Removed = append(change.Removed, &model.Label{ID: id})
		}
	}
	if len(change.Added) == 0 && len(change.Removed) == 0 {
		return false, nil
	}
	if len(current)+len(change.Added)-len(change.Removed) > model.MaxLabelsPerIssue {
		return false, model.TooManyLabelsError()
	}
	if err := fillLabelNames(tx, change.Removed); err != nil {
		return false, err
	}
	sort.Slice(change.Removed, func(i, j int) bool {
		return strings.ToLower(change.Removed[i].Name) < strings.ToLower(change.Removed[j].Name)
	})
	if len(change.Removed) > 0 {
		ids := make([]string, len(change.Removed))
		for i, l := range change.Removed {
			ids[i] = l.ID
		}
		if err := tx.Exec(`DELETE FROM issues.issue_labels WHERE issue_id = ? AND label_id IN ? AND imported`, issue.ID, ids).Error; err != nil {
			return false, err
		}
	}
	for _, l := range change.Added {
		if err := tx.Exec(`INSERT INTO issues.issue_labels (issue_id, label_id, imported) VALUES (?, ?, true)`, issue.ID, l.ID).Error; err != nil {
			return false, err
		}
	}
	return true, insertEvent(tx, issue.ID, author, &model.IssueEvent{Kind: model.EventLabelsChanged, Labels: change}, now)
}

// ensureLabels devuelve las etiquetas del proyecto con esos nombres (sin
// distinguir mayúsculas), en el orden de names, y crea las que faltan con
// color neutral respetando el máximo del proyecto. El lock del proyecto ya
// está tomado.
func ensureLabels(tx *gorm.DB, projectID string, names []string, now time.Time) ([]*model.Label, error) {
	if len(names) == 0 {
		return nil, nil
	}
	lower := make([]string, len(names))
	for i, n := range names {
		lower[i] = strings.ToLower(n)
	}
	var rows []labelRow
	if err := tx.Where("project_id = ? AND lower(name) IN ?", projectID, lower).Find(&rows).Error; err != nil {
		return nil, err
	}
	byName := make(map[string]*model.Label, len(rows))
	for i := range rows {
		byName[strings.ToLower(rows[i].Name)] = fromLabelRow(&rows[i])
	}
	var count int64
	counted := false
	out := make([]*model.Label, 0, len(names))
	for i, name := range names {
		if l, ok := byName[lower[i]]; ok {
			out = append(out, l)
			continue
		}
		if !counted {
			if err := tx.Model(&labelRow{}).Where("project_id = ?", projectID).Count(&count).Error; err != nil {
				return nil, err
			}
			counted = true
		}
		if count >= model.MaxLabelsPerProject {
			return nil, domainerrors.FailedPreconditionError("project has the maximum number of labels", map[string]any{"max": model.MaxLabelsPerProject})
		}
		row := &labelRow{ID: uuid.NewString(), ProjectID: projectID, Name: name, Color: int(model.LabelColorNeutral), CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(row).Error; err != nil {
			return nil, err
		}
		count++
		l := fromLabelRow(row)
		byName[lower[i]] = l
		out = append(out, l)
	}
	return out, nil
}

// fillLabelNames completa el nombre de etiquetas que solo traen el id.
func fillLabelNames(tx *gorm.DB, labels []*model.Label) error {
	if len(labels) == 0 {
		return nil
	}
	ids := make([]string, len(labels))
	for i, l := range labels {
		ids[i] = l.ID
	}
	var rows []labelRow
	if err := tx.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return err
	}
	names := make(map[string]string, len(rows))
	for _, r := range rows {
		names[r.ID] = r.Name
	}
	for _, l := range labels {
		l.Name = names[l.ID]
	}
	return nil
}

func statusByID(statuses []*model.Status, id string) *model.Status {
	for _, st := range statuses {
		if st.ID == id {
			return st
		}
	}
	return nil
}

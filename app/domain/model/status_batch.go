package model

import (
	"strconv"
	"strings"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// StatusSpec es un estado tal como debe quedar en BatchUpdateStatuses. ID
// vacío = estado nuevo.
type StatusSpec struct {
	ID       string
	Name     string
	Category IssueState
	Color    LabelColor
	WIPLimit int32
	Hidden   bool
}

// StatusDeletion es un estado que se borra en BatchUpdateStatuses y el
// estado al que pasan sus issues ("" si no tiene).
type StatusDeletion struct {
	StatusID       string
	MoveToStatusID string
}

// StatusBatchPlan es el resultado validado de BatchUpdateStatuses.
type StatusBatchPlan struct {
	// Final son los estados como deben quedar, en el orden de columnas
	// (Rank vacío: lo asigna quien guarda). Los nuevos tienen ID nuevo.
	Final []*Status
	// IsNew informa, por ID, qué estados de Final se crean.
	IsNew map[string]bool
	// Deletions son los estados que se borran, con su destino ya validado
	// (un estado existente que queda en Final).
	Deletions []StatusDeletion
}

// PlanStatusBatch valida BatchUpdateStatuses contra los estados actuales del
// proyecto (current) y devuelve qué hay que guardar. newID genera los IDs de
// los estados nuevos. No mira los issues: que un estado borrado sin destino
// no tenga issues lo verifica quien guarda.
//
// Errores, en este orden:
//   - ValidationError: nombre, color o WIP inválidos; un id que no es del
//     proyecto o repetido; una categoría que cambia, falta o no existe; un
//     estado en statuses y en deletions; un destino que no queda en la lista.
//   - FailedPreconditionError (reason STALE_STATUSES): un estado del
//     proyecto que no está en ninguna de las dos listas (vista vieja).
//   - FailedPreconditionError: más de MaxStatusesPerProject, o una categoría
//     sin estados (reason LAST_STATUS_OF_CATEGORY).
//   - ConflictError (reason STATUS_NAME_TAKEN): nombres repetidos.
func PlanStatusBatch(current []*Status, specs []StatusSpec, deletions []StatusDeletion, newID func() string) (*StatusBatchPlan, error) {
	byID := make(map[string]*Status, len(current))
	for _, s := range current {
		byID[s.ID] = s
	}
	plan := &StatusBatchPlan{IsNew: map[string]bool{}}
	kept := make(map[string]bool, len(specs))

	for i, spec := range specs {
		field := "statuses[" + strconv.Itoa(i) + "]"
		name, err := NormalizeStatusName(spec.Name)
		if err != nil {
			return nil, err
		}
		if err := ValidateWIPLimit(spec.WIPLimit); err != nil {
			return nil, err
		}
		st := &Status{Name: name, WIPLimit: spec.WIPLimit, Hidden: spec.Hidden}
		if spec.ID == "" {
			if spec.Category == IssueStateUnspecified {
				return nil, domainerrors.ValidationError("category is required for a new status", map[string]any{"field": field + ".category"})
			}
			if !spec.Category.IsValid() {
				return nil, domainerrors.ValidationError("invalid category", map[string]any{"field": field + ".category", "value": string(spec.Category)})
			}
			st.ID, st.Category = newID(), spec.Category
			plan.IsNew[st.ID] = true
		} else {
			cur, ok := byID[spec.ID]
			if !ok {
				return nil, domainerrors.ValidationError("status is not part of the project", map[string]any{"field": field + ".id", "value": spec.ID})
			}
			if kept[spec.ID] {
				return nil, domainerrors.ValidationError("status is repeated", map[string]any{"field": field + ".id", "value": spec.ID})
			}
			if spec.Category != IssueStateUnspecified && spec.Category != cur.Category {
				// También un valor desconocido: no es la categoría del estado.
				return nil, domainerrors.ValidationError("the category of a status cannot change", map[string]any{"field": field + ".category"})
			}
			st.ID, st.Category, st.ProjectID = cur.ID, cur.Category, cur.ProjectID
			kept[cur.ID] = true
		}
		if st.Color, err = ResolveStatusColor(spec.Color, st.Category); err != nil {
			return nil, err
		}
		plan.Final = append(plan.Final, st)
	}

	deleted := make(map[string]bool, len(deletions))
	for i, d := range deletions {
		field := "deletions[" + strconv.Itoa(i) + "]"
		if _, ok := byID[d.StatusID]; !ok {
			return nil, domainerrors.ValidationError("status is not part of the project", map[string]any{"field": field + ".status_id", "value": d.StatusID})
		}
		if deleted[d.StatusID] {
			return nil, domainerrors.ValidationError("status is repeated", map[string]any{"field": field + ".status_id", "value": d.StatusID})
		}
		if kept[d.StatusID] {
			return nil, domainerrors.ValidationError("a status cannot be kept and deleted", map[string]any{"field": field + ".status_id", "value": d.StatusID})
		}
		if d.MoveToStatusID != "" && !kept[d.MoveToStatusID] {
			return nil, domainerrors.ValidationError("move_to_status_id must be an existing status that is kept",
				map[string]any{"field": field + ".move_to_status_id", "value": d.MoveToStatusID})
		}
		deleted[d.StatusID] = true
		plan.Deletions = append(plan.Deletions, d)
	}

	for _, s := range current {
		if !kept[s.ID] && !deleted[s.ID] {
			return nil, domainerrors.FailedPreconditionError("the statuses changed: reload and try again", map[string]any{"status_id": s.ID}).
				WithReason("STALE_STATUSES")
		}
	}
	if len(plan.Final) > MaxStatusesPerProject {
		return nil, domainerrors.FailedPreconditionError("project has the maximum number of statuses", map[string]any{"max": MaxStatusesPerProject})
	}
	for _, category := range []IssueState{IssueStateTodo, IssueStateInProgress, IssueStateDone, IssueStateCanceled} {
		if FirstStatusOf(plan.Final, category) == nil {
			return nil, domainerrors.FailedPreconditionError("every category needs a status", map[string]any{"category": string(category)}).
				WithReason("LAST_STATUS_OF_CATEGORY")
		}
	}
	names := make(map[string]bool, len(plan.Final))
	for _, s := range plan.Final {
		key := strings.ToLower(s.Name)
		if names[key] {
			return nil, domainerrors.ConflictError("status name already exists", map[string]any{"name": s.Name}).
				WithReason("STATUS_NAME_TAKEN")
		}
		names[key] = true
	}
	return plan, nil
}

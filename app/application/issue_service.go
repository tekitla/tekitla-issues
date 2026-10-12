package application

import (
	"context"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// IssueService implementa los casos de uso de Issue, sus comentarios y su
// historial.
//
// Todo caso de uso verifica primero que el proyecto sea de un espacio de
// ownerUID: un proyecto ajeno responde NotFound, igual que uno inexistente,
// para no revelar que existe. ownerUID es también el autor de los cambios.
type IssueService struct {
	guard    projectGuard
	issues   port.IssueRepository
	statuses port.StatusRepository
	labels   port.LabelRepository
}

// NewIssueService construye el servicio.
func NewIssueService(
	workspaceRepo port.WorkspaceRepository,
	projects port.ProjectRepository,
	issues port.IssueRepository,
	statuses port.StatusRepository,
	labels port.LabelRepository,
) *IssueService {
	return &IssueService{
		guard:    projectGuard{workspaces: workspaces{repo: workspaceRepo}, projects: projects},
		issues:   issues,
		statuses: statuses,
		labels:   labels,
	}
}

// ListIssuesInput son los datos que acepta ListIssues: filtros (Y entre sí,
// O dentro de cada lista; vacío = no filtra), búsqueda y orden. State es una
// sola categoría (clientes de v0.3); no se puede mandar con States.
type ListIssuesInput struct {
	ProjectID  string
	State      model.IssueState
	States     []model.IssueState
	StatusIDs  []string
	Priorities []model.Priority
	LabelIDs   []string
	Query      string
	// Order vacío = rank.
	Order  model.IssueOrder
	Cursor string
	Limit  int32
}

// DiscardIssueInput son los datos que acepta DiscardIssue. StatusID vacío =
// el primer estado CANCELED del proyecto.
type DiscardIssueInput struct {
	ProjectID string
	Number    int32
	Reason    string
	StatusID  string
}

// CreateIssueInput son los datos que acepta CreateIssue.
// Priority sin especificar se guarda como model.DefaultPriority.
type CreateIssueInput struct {
	ProjectID   string
	Title       string
	Description string
	Priority    model.Priority
}

// MoveIssueInput son los datos que acepta MoveIssue. StatusID manda sobre
// State; State solo (clientes que no conocen los estados del proyecto) va al
// primer estado de esa categoría, o no cambia si el issue ya está en ella.
type MoveIssueInput struct {
	ProjectID string
	Number    int32
	State     model.IssueState
	StatusID  string
	Place     port.Placement
}

// UpdateIssueInput son los datos que acepta UpdateIssue: nil = no cambia.
type UpdateIssueInput struct {
	ProjectID   string
	Number      int32
	Title       *string
	Description *string
	Priority    *model.Priority
	// LabelIDs reemplaza las etiquetas (nil = no cambian).
	LabelIDs *[]string
}

// ListIssues devuelve una página de issues del proyecto con la búsqueda,
// los filtros y el orden pedidos (sin ellos: todos, por rank).
func (s *IssueService) ListIssues(ctx context.Context, ownerUID string, in ListIssuesInput) (*port.IssuePage, error) {
	project, _, err := s.guard.project(ctx, ownerUID, in.ProjectID)
	if err != nil {
		return nil, err
	}
	filter := port.IssueFilter{ProjectID: in.ProjectID, Order: in.Order, Cursor: in.Cursor, Limit: normalizeLimit(in.Limit)}

	states := in.States
	if in.State != model.IssueStateUnspecified {
		if len(in.States) > 0 {
			return nil, domainerrors.ValidationError("state and states cannot be used together", map[string]any{"field": "states"})
		}
		states = []model.IssueState{in.State}
	}
	for _, st := range unique(states) {
		if !st.IsValid() {
			return nil, domainerrors.ValidationError("invalid state", map[string]any{"field": "states", "value": string(st)})
		}
		filter.States = append(filter.States, st)
	}
	for _, p := range unique(in.Priorities) {
		if !p.IsValid() {
			return nil, domainerrors.ValidationError("invalid priority", map[string]any{"field": "priorities", "value": int(p)})
		}
		filter.Priorities = append(filter.Priorities, p)
	}
	if filter.Order == model.IssueOrderUnspecified {
		filter.Order = model.IssueOrderRank
	}
	if !filter.Order.IsValid() {
		return nil, domainerrors.ValidationError("invalid order", map[string]any{"field": "order_by", "value": string(in.Order)})
	}
	if filter.Search, err = model.ParseIssueSearch(in.Query, project.Key); err != nil {
		return nil, err
	}
	if len(in.StatusIDs) > 0 {
		statuses, err := s.statuses.List(ctx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		if filter.StatusIDs, err = projectIDs("status_ids", unique(in.StatusIDs), statusIDSet(statuses)); err != nil {
			return nil, err
		}
	}
	if len(in.LabelIDs) > 0 {
		labels, err := s.labels.List(ctx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		known := make(map[string]bool, len(labels))
		for _, l := range labels {
			known[l.ID] = true
		}
		if filter.LabelIDs, err = projectIDs("label_ids", unique(in.LabelIDs), known); err != nil {
			return nil, err
		}
	}
	return s.issues.List(ctx, filter)
}

// GetIssue devuelve un issue por su número dentro del proyecto.
func (s *IssueService) GetIssue(ctx context.Context, ownerUID, projectID string, number int32) (*model.Issue, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, projectID); err != nil {
		return nil, err
	}
	if err := validateIssueNumber(number); err != nil {
		return nil, err
	}
	return s.issues.GetByNumber(ctx, projectID, number)
}

// CreateIssue crea un issue en el primer estado «por hacer» del proyecto, con
// el siguiente número y al final de su orden.
func (s *IssueService) CreateIssue(ctx context.Context, ownerUID string, in CreateIssueInput) (*model.Issue, error) {
	workspaceIDs, err := s.guard.ensure(ctx, ownerUID, in.ProjectID)
	if err != nil {
		return nil, err
	}
	title, err := model.NormalizeIssueTitle(in.Title)
	if err != nil {
		return nil, err
	}
	desc, err := model.NormalizeIssueDescription(in.Description)
	if err != nil {
		return nil, err
	}
	priority := in.Priority
	if priority == model.PriorityUnspecified {
		priority = model.DefaultPriority
	}
	if !priority.IsValid() {
		return nil, invalidPriority(in.Priority)
	}
	statuses, err := s.statuses.List(ctx, in.ProjectID)
	if err != nil {
		return nil, err
	}
	todo := model.FirstStatusOf(statuses, model.IssueStateTodo)
	if todo == nil {
		// DeleteStatus nunca deja una categoría vacía: es un problema de datos.
		return nil, domainerrors.InternalError("project has no todo status", map[string]any{"project_id": in.ProjectID})
	}

	issue := &model.Issue{
		ID:          uuid.NewString(),
		ProjectID:   in.ProjectID,
		Title:       title,
		Description: desc,
		State:       todo.Category,
		StatusID:    todo.ID,
		Priority:    priority,
	}
	return s.issues.Create(ctx, workspaceIDs, issue, ownerUID)
}

// MoveIssue cambia el estado de un issue y, opcionalmente, su lugar en la
// columna destino.
func (s *IssueService) MoveIssue(ctx context.Context, ownerUID string, in MoveIssueInput) (*model.Issue, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, in.ProjectID); err != nil {
		return nil, err
	}
	if err := validateIssueNumber(in.Number); err != nil {
		return nil, err
	}
	if err := validatePlacement(in.Number, in.Place); err != nil {
		return nil, err
	}

	var target *model.Status
	if in.StatusID != "" {
		if err := validateUUID("status_id", in.StatusID); err != nil {
			return nil, err
		}
		status, err := s.statuses.Get(ctx, in.ProjectID, in.StatusID)
		if domainerrors.IsNotFound(err) {
			return nil, domainerrors.ValidationError("status is not part of the project", map[string]any{"field": "status_id", "value": in.StatusID})
		}
		if err != nil {
			return nil, err
		}
		target = status
	} else {
		if !in.State.IsValid() {
			return nil, invalidState(in.State)
		}
		issue, err := s.issues.GetByNumber(ctx, in.ProjectID, in.Number)
		if err != nil {
			return nil, err
		}
		statuses, err := s.statuses.List(ctx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		if issue.State == in.State {
			target = statusByID(statuses, issue.StatusID)
		} else {
			target = model.FirstStatusOf(statuses, in.State)
		}
		if target == nil {
			return nil, domainerrors.InternalError("project has no status for the category", map[string]any{"project_id": in.ProjectID, "state": string(in.State)})
		}
	}
	return s.issues.Move(ctx, in.ProjectID, in.Number, target, in.Place, ownerUID)
}

// DiscardIssue descarta un issue con un motivo: lo pasa a un estado CANCELED
// (el pedido o el primero) conservando su lugar.
func (s *IssueService) DiscardIssue(ctx context.Context, ownerUID string, in DiscardIssueInput) (*model.Issue, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, in.ProjectID); err != nil {
		return nil, err
	}
	if err := validateIssueNumber(in.Number); err != nil {
		return nil, err
	}
	reason, err := model.NormalizeDiscardReason(in.Reason)
	if err != nil {
		return nil, err
	}
	var target *model.Status
	if in.StatusID != "" {
		if err := validateUUID("status_id", in.StatusID); err != nil {
			return nil, err
		}
		status, err := s.statuses.Get(ctx, in.ProjectID, in.StatusID)
		if domainerrors.IsNotFound(err) {
			return nil, domainerrors.ValidationError("status is not part of the project", map[string]any{"field": "status_id", "value": in.StatusID})
		}
		if err != nil {
			return nil, err
		}
		if status.Category != model.IssueStateCanceled {
			return nil, domainerrors.ValidationError("status is not a canceled status", map[string]any{"field": "status_id", "value": in.StatusID})
		}
		target = status
	} else if target, err = s.firstStatusOf(ctx, in.ProjectID, model.IssueStateCanceled); err != nil {
		return nil, err
	}
	return s.issues.Discard(ctx, in.ProjectID, in.Number, target, reason, ownerUID)
}

// RestoreIssue restaura un issue descartado: lo pasa al primer estado «por
// hacer», al final de esa columna.
func (s *IssueService) RestoreIssue(ctx context.Context, ownerUID, projectID string, number int32) (*model.Issue, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, projectID); err != nil {
		return nil, err
	}
	if err := validateIssueNumber(number); err != nil {
		return nil, err
	}
	target, err := s.firstStatusOf(ctx, projectID, model.IssueStateTodo)
	if err != nil {
		return nil, err
	}
	return s.issues.Restore(ctx, projectID, number, target, ownerUID)
}

// firstStatusOf devuelve el primer estado de la categoría. Todo proyecto
// tiene al menos uno por categoría: si falta, es un problema de datos.
func (s *IssueService) firstStatusOf(ctx context.Context, projectID string, category model.IssueState) (*model.Status, error) {
	statuses, err := s.statuses.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	st := model.FirstStatusOf(statuses, category)
	if st == nil {
		return nil, domainerrors.InternalError("project has no status for the category", map[string]any{"project_id": projectID, "state": string(category)})
	}
	return st, nil
}

// UpdateIssue edita título, descripción, prioridad o etiquetas de un issue.
// Sin cambios pedidos devuelve el issue tal cual.
func (s *IssueService) UpdateIssue(ctx context.Context, ownerUID string, in UpdateIssueInput) (*model.Issue, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, in.ProjectID); err != nil {
		return nil, err
	}
	if err := validateIssueNumber(in.Number); err != nil {
		return nil, err
	}
	patch, err := s.buildPatch(ctx, in)
	if err != nil {
		return nil, err
	}
	if patch.IsEmpty() {
		return s.issues.GetByNumber(ctx, in.ProjectID, in.Number)
	}
	return s.issues.Update(ctx, in.ProjectID, in.Number, patch, ownerUID)
}

func (s *IssueService) buildPatch(ctx context.Context, in UpdateIssueInput) (model.IssuePatch, error) {
	var patch model.IssuePatch
	if in.Title != nil {
		title, err := model.NormalizeIssueTitle(*in.Title)
		if err != nil {
			return patch, err
		}
		patch.Title = &title
	}
	if in.Description != nil {
		desc, err := model.NormalizeIssueDescription(*in.Description)
		if err != nil {
			return patch, err
		}
		patch.Description = &desc
	}
	if in.Priority != nil {
		if !in.Priority.IsValid() {
			return patch, invalidPriority(*in.Priority)
		}
		priority := *in.Priority
		patch.Priority = &priority
	}
	if in.LabelIDs != nil {
		ids, err := s.projectLabelIDs(ctx, in.ProjectID, *in.LabelIDs)
		if err != nil {
			return patch, err
		}
		patch.LabelIDs = &ids
	}
	return patch, nil
}

// projectLabelIDs valida que las etiquetas sean del proyecto y quita repetidos.
func (s *IssueService) projectLabelIDs(ctx context.Context, projectID string, ids []string) ([]string, error) {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) > model.MaxLabelsPerIssue {
		return nil, domainerrors.ValidationError("too many labels", map[string]any{"field": "labels", "max": model.MaxLabelsPerIssue})
	}
	if len(unique) == 0 {
		return unique, nil
	}
	labels, err := s.labels.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(labels))
	for _, l := range labels {
		known[l.ID] = true
	}
	for _, id := range unique {
		if !known[id] {
			return nil, domainerrors.ValidationError("label is not part of the project", map[string]any{"field": "labels", "value": id})
		}
	}
	return unique, nil
}

// CreateComment comenta un issue.
func (s *IssueService) CreateComment(ctx context.Context, ownerUID, projectID string, number int32, body string) (*model.Comment, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, projectID); err != nil {
		return nil, err
	}
	if err := validateIssueNumber(number); err != nil {
		return nil, err
	}
	text, err := model.NormalizeCommentBody(body)
	if err != nil {
		return nil, err
	}
	return s.issues.AddComment(ctx, projectID, number, &model.Comment{
		ID:        uuid.NewString(),
		AuthorUID: ownerUID,
		Body:      text,
	})
}

// ListIssueEvents devuelve una página del historial de un issue, del más
// viejo al más nuevo.
func (s *IssueService) ListIssueEvents(ctx context.Context, ownerUID, projectID string, number int32, page port.PageFilter) (*port.EventPage, error) {
	if _, err := s.guard.ensure(ctx, ownerUID, projectID); err != nil {
		return nil, err
	}
	if err := validateIssueNumber(number); err != nil {
		return nil, err
	}
	page.Limit = normalizeLimit(page.Limit)
	return s.issues.ListEvents(ctx, projectID, number, page)
}

// validatePlacement valida los vecinos de MoveIssue: no negativos, distintos
// entre sí y del propio issue.
func validatePlacement(number int32, place port.Placement) error {
	for field, n := range map[string]int32{"after_number": place.AfterNumber, "before_number": place.BeforeNumber} {
		if n < 0 {
			return domainerrors.ValidationError(field+" must not be negative", map[string]any{"field": field, "value": n})
		}
		if n == number {
			return domainerrors.ValidationError(field+" must be another issue", map[string]any{"field": field, "value": n})
		}
	}
	if place.AfterNumber != 0 && place.AfterNumber == place.BeforeNumber {
		return domainerrors.ValidationError("after_number and before_number must differ", map[string]any{"field": "before_number"})
	}
	return nil
}

// unique quita repetidos conservando el orden.
func unique[T comparable](values []T) []T {
	out := make([]T, 0, len(values))
	seen := make(map[T]bool, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// projectIDs verifica que cada id esté en known (los del proyecto); uno que
// no está es ValidationError sobre field.
func projectIDs(field string, ids []string, known map[string]bool) ([]string, error) {
	for _, id := range ids {
		if !known[id] {
			return nil, domainerrors.ValidationError("id is not part of the project", map[string]any{"field": field, "value": id})
		}
	}
	return ids, nil
}

func statusIDSet(statuses []*model.Status) map[string]bool {
	set := make(map[string]bool, len(statuses))
	for _, st := range statuses {
		set[st.ID] = true
	}
	return set
}

func statusByID(statuses []*model.Status, id string) *model.Status {
	for _, st := range statuses {
		if st.ID == id {
			return st
		}
	}
	return nil
}

func invalidState(state model.IssueState) error {
	if state == model.IssueStateUnspecified {
		return domainerrors.ValidationError("state is required", map[string]any{"field": "state"})
	}
	return domainerrors.ValidationError("invalid state", map[string]any{"field": "state", "value": string(state)})
}

func invalidPriority(p model.Priority) error {
	return domainerrors.ValidationError("invalid priority", map[string]any{"field": "priority", "value": int(p)})
}

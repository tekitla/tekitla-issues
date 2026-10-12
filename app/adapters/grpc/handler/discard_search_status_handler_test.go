package handler_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// Repos en memoria de los issues 011 a 013 (v0.4.0): descartar y restaurar,
// filtros de ListIssues y BatchUpdateStatuses. Como en la 3a, la lógica de
// verdad (ranks, LIKE sin acentos, índices únicos a mitad del batch) la
// cubren el repo de Postgres y la prueba con grpcurl.

// memMatches aplica los filtros de ListIssues (sin orden ni paginación).
func memMatches(it *model.Issue, f port.IssueFilter) bool {
	if len(f.States) > 0 && !slices.Contains(f.States, it.State) {
		return false
	}
	if len(f.StatusIDs) > 0 && !slices.Contains(f.StatusIDs, it.StatusID) {
		return false
	}
	if len(f.Priorities) > 0 && !slices.Contains(f.Priorities, it.Priority) {
		return false
	}
	if len(f.LabelIDs) > 0 && !slices.ContainsFunc(it.LabelIDs, func(id string) bool { return slices.Contains(f.LabelIDs, id) }) {
		return false
	}
	if s := f.Search; s.Text != "" {
		return strings.Contains(strings.ToLower(it.Title), strings.ToLower(s.Text)) || (s.Number > 0 && it.Number == s.Number)
	}
	return true
}

func (s memIssues) Discard(_ context.Context, projectID string, number int32, statusID, reason, actor string) (*model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := s.status(projectID, statusID)
	if statusID == "" {
		target = model.FirstStatusOf(s.projectStatuses(projectID), model.IssueStateCanceled)
	}
	it := s.find(projectID, number)
	if it == nil {
		return nil, domainerrors.NotFoundError("issue not found", nil)
	}
	if it.State == model.IssueStateCanceled {
		return nil, domainerrors.FailedPreconditionError("issue is already discarded", nil).WithReason("ISSUE_ALREADY_DISCARDED")
	}
	from := s.status(projectID, it.StatusID)
	s.addEvent(it.ID, actor, &model.IssueEvent{Kind: model.EventDiscarded, Status: model.NewStatusChange(from, target), Reason: reason})
	it.StatusID, it.State = target.ID, target.Category
	it.Discard = &model.DiscardInfo{Reason: reason, ActorUID: actor}
	return it, nil
}

func (s memIssues) Restore(_ context.Context, projectID string, number int32, actor string) (*model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := model.FirstStatusOf(s.projectStatuses(projectID), model.IssueStateTodo)
	it := s.find(projectID, number)
	if it == nil {
		return nil, domainerrors.NotFoundError("issue not found", nil)
	}
	if it.State != model.IssueStateCanceled {
		return nil, domainerrors.FailedPreconditionError("issue is not discarded", nil).WithReason("ISSUE_NOT_DISCARDED")
	}
	from := s.status(projectID, it.StatusID)
	s.addEvent(it.ID, actor, &model.IssueEvent{Kind: model.EventRestored, Status: model.NewStatusChange(from, target)})
	it.StatusID, it.State, it.Discard = target.ID, target.Category, nil
	return it, nil
}

func (s memStatuses) BatchUpdate(_ context.Context, projectID string, specs []model.StatusSpec, deletions []model.StatusDeletion, actor string) ([]*model.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, err := model.PlanStatusBatch(s.projectStatuses(projectID), specs, deletions, uuid.NewString)
	if err != nil {
		return nil, err
	}
	final := map[string]*model.Status{}
	rank := ""
	for _, st := range plan.Final {
		rank, _ = model.RankAfter(rank)
		st.ProjectID, st.Rank = projectID, rank
		final[st.ID] = st
	}
	for _, d := range plan.Deletions {
		from := s.status(projectID, d.StatusID)
		for _, it := range s.issues {
			if it.StatusID != d.StatusID {
				continue
			}
			to := final[d.MoveToStatusID]
			if to == nil {
				return nil, domainerrors.FailedPreconditionError("status has issues", nil).WithReason("STATUS_HAS_ISSUES")
			}
			s.addEvent(it.ID, actor, &model.IssueEvent{Kind: model.EventStatusChanged, Status: model.NewStatusChange(from, to)})
			it.StatusID, it.State = to.ID, to.Category
		}
	}
	s.statuses = slices.DeleteFunc(s.statuses, func(st *model.Status) bool { return st.ProjectID == projectID })
	s.statuses = append(s.statuses, plan.Final...)
	return s.projectStatuses(projectID), nil
}

// --- Recorrido de 011 a 013 ---

func TestIssueHandler_DiscardSearchStatusFlow(t *testing.T) {
	client := newClient(t)
	ctx := asUser(t, "u1")

	created, err := client.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "TK", Name: "Tekitla"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := created.GetProject().GetId()

	// 013: los estados nacen con el color de su categoría; CANCELED oculto.
	statuses, err := client.ListStatuses(ctx, &issuesv1.ListStatusesRequest{ProjectId: pid})
	if err != nil {
		t.Fatalf("ListStatuses: %v", err)
	}
	all := statuses.GetStatuses()
	todo, doing, done, discarded := all[0], all[1], all[2], all[3]
	if todo.GetColor() != issuesv1.LabelColor_LABEL_COLOR_NEUTRAL || discarded.GetColor() != issuesv1.LabelColor_LABEL_COLOR_DANGER ||
		!discarded.GetHidden() || todo.GetHidden() {
		t.Errorf("default statuses: got %+v", all)
	}

	for _, title := range []string{"Búsqueda", "Filtros", "Orden 12"} {
		if _, err := client.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: title}); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
	}

	// 011: descartar con motivo y restaurar.
	disc, err := client.DiscardIssue(ctx, &issuesv1.DiscardIssueRequest{ProjectId: pid, Number: 2, Reason: "  duplicado de TK-1  "})
	if err != nil {
		t.Fatalf("DiscardIssue: %v", err)
	}
	if got := disc.GetIssue(); got.GetStatusId() != discarded.GetId() || got.GetDiscard().GetReason() != "duplicado de TK-1" || got.GetDiscard().GetActorId() != "u1" {
		t.Errorf("DiscardIssue: got %+v", got)
	}
	_, err = client.DiscardIssue(ctx, &issuesv1.DiscardIssueRequest{ProjectId: pid, Number: 2, Reason: "otra vez"})
	wantCode(t, "DiscardIssue ya descartado", err, codes.FailedPrecondition)
	_, err = client.DiscardIssue(ctx, &issuesv1.DiscardIssueRequest{ProjectId: pid, Number: 1, Reason: "   "})
	wantCode(t, "DiscardIssue sin motivo", err, codes.InvalidArgument)
	_, err = client.DiscardIssue(ctx, &issuesv1.DiscardIssueRequest{ProjectId: pid, Number: 1, Reason: "x", StatusId: done.GetId()})
	wantCode(t, "DiscardIssue a un estado que no es CANCELED", err, codes.InvalidArgument)
	_, err = client.RestoreIssue(ctx, &issuesv1.RestoreIssueRequest{ProjectId: pid, Number: 1})
	wantCode(t, "RestoreIssue no descartado", err, codes.FailedPrecondition)

	restored, err := client.RestoreIssue(ctx, &issuesv1.RestoreIssueRequest{ProjectId: pid, Number: 2})
	if err != nil || restored.GetIssue().GetStatusId() != todo.GetId() || restored.GetIssue().GetDiscard() != nil {
		t.Fatalf("RestoreIssue: got %+v, err %v", restored.GetIssue(), err)
	}
	events, err := client.ListIssueEvents(ctx, &issuesv1.ListIssueEventsRequest{ProjectId: pid, Number: 2})
	if err != nil || len(events.GetEvents()) != 3 {
		t.Fatalf("ListIssueEvents: got %+v, err %v", events, err)
	}
	if d := events.GetEvents()[1].GetDiscarded(); d.GetReason() != "duplicado de TK-1" || d.GetFromStatusName() != "Por hacer" || d.GetToStatusName() != "Descartado" {
		t.Errorf("evento discarded: got %+v", events.GetEvents()[1])
	}
	if r := events.GetEvents()[2].GetRestored(); r.GetFromStatusId() != discarded.GetId() || r.GetToStatusId() != todo.GetId() {
		t.Errorf("evento restored: got %+v", events.GetEvents()[2])
	}

	// Descartar con MoveIssue: sin motivo; sacarlo de CANCELED lo borra.
	if _, err := client.DiscardIssue(ctx, &issuesv1.DiscardIssueRequest{ProjectId: pid, Number: 3, Reason: "no va"}); err != nil {
		t.Fatalf("DiscardIssue 3: %v", err)
	}
	back, err := client.MoveIssue(ctx, &issuesv1.MoveIssueRequest{ProjectId: pid, Number: 3, StatusId: doing.GetId()})
	if err != nil || back.GetIssue().GetDiscard() != nil {
		t.Errorf("MoveIssue fuera de CANCELED: got %+v, err %v", back.GetIssue(), err)
	}

	// 012: búsqueda y filtros.
	byKey, err := client.ListIssues(ctx, &issuesv1.ListIssuesRequest{ProjectId: pid, Query: "tk-1"})
	if err != nil || len(byKey.GetIssues()) != 1 || byKey.GetIssues()[0].GetNumber() != 1 {
		t.Errorf("ListIssues query tk-1: got %+v, err %v", byKey.GetIssues(), err)
	}
	byNumber, err := client.ListIssues(ctx, &issuesv1.ListIssuesRequest{ProjectId: pid, Query: "12"})
	if err != nil || len(byNumber.GetIssues()) != 1 || byNumber.GetIssues()[0].GetNumber() != 3 {
		t.Errorf("ListIssues query 12 (título «Orden 12»): got %+v, err %v", byNumber.GetIssues(), err)
	}
	active, err := client.ListIssues(ctx, &issuesv1.ListIssuesRequest{ProjectId: pid,
		States: []issuesv1.IssueState{issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS}})
	if err != nil || len(active.GetIssues()) != 1 || active.GetIssues()[0].GetNumber() != 3 {
		t.Errorf("ListIssues states: got %+v, err %v", active.GetIssues(), err)
	}
	_, err = client.ListIssues(ctx, &issuesv1.ListIssuesRequest{ProjectId: pid, State: issuesv1.IssueState_ISSUE_STATE_TODO,
		States: []issuesv1.IssueState{issuesv1.IssueState_ISSUE_STATE_DONE}})
	wantCode(t, "ListIssues state + states", err, codes.InvalidArgument)
	_, err = client.ListIssues(ctx, &issuesv1.ListIssuesRequest{ProjectId: pid, StatusIds: []string{uuid.NewString()}})
	wantCode(t, "ListIssues status_ids ajeno", err, codes.InvalidArgument)
	_, err = client.ListIssues(ctx, &issuesv1.ListIssuesRequest{ProjectId: pid, Query: strings.Repeat("a", 201)})
	wantCode(t, "ListIssues query largo", err, codes.InvalidArgument)
	_, err = client.ListIssues(ctx, &issuesv1.ListIssuesRequest{ProjectId: pid, OrderBy: issuesv1.IssueOrder(99)})
	wantCode(t, "ListIssues orden desconocido", err, codes.InvalidArgument)

	// 013: color, WIP y visibilidad sueltos.
	wip := int32(3)
	upd, err := client.UpdateStatus(ctx, &issuesv1.UpdateStatusRequest{ProjectId: pid, StatusId: doing.GetId(), WipLimit: &wip})
	if err != nil || upd.GetStatus().GetWipLimit() != 3 || upd.GetStatus().GetColor() != issuesv1.LabelColor_LABEL_COLOR_INFO {
		t.Errorf("UpdateStatus wip: got %+v, err %v", upd.GetStatus(), err)
	}
	neg := int32(-1)
	_, err = client.UpdateStatus(ctx, &issuesv1.UpdateStatusRequest{ProjectId: pid, StatusId: doing.GetId(), WipLimit: &neg})
	wantCode(t, "UpdateStatus wip negativo", err, codes.InvalidArgument)

	// 013: BatchUpdateStatuses. Renombra, reordena, crea y borra moviendo
	// issues; un estado que falta es vista vieja.
	_, err = client.BatchUpdateStatuses(ctx, &issuesv1.BatchUpdateStatusesRequest{ProjectId: pid, Statuses: []*issuesv1.StatusSpec{
		{Id: todo.GetId(), Name: "Backlog"}, {Id: doing.GetId(), Name: "En curso"}, {Id: done.GetId(), Name: "Hecho"},
	}})
	wantCode(t, "BatchUpdateStatuses sin un estado", err, codes.FailedPrecondition)

	batch, err := client.BatchUpdateStatuses(ctx, &issuesv1.BatchUpdateStatusesRequest{ProjectId: pid,
		Statuses: []*issuesv1.StatusSpec{
			{Id: todo.GetId(), Name: "Backlog", Hidden: false},
			{Name: "Revisión", Category: issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS, WipLimit: 2},
			{Id: done.GetId(), Name: "Hecho", Color: issuesv1.LabelColor_LABEL_COLOR_ACCENT},
			{Id: discarded.GetId(), Name: "Descartado", Hidden: true},
		},
		Deletions: []*issuesv1.StatusDeletion{{StatusId: doing.GetId(), MoveToStatusId: done.GetId()}},
	})
	if err != nil {
		t.Fatalf("BatchUpdateStatuses: %v", err)
	}
	var names []string
	for _, st := range batch.GetStatuses() {
		names = append(names, st.GetName())
	}
	if !slices.Equal(names, []string{"Backlog", "Revisión", "Hecho", "Descartado"}) {
		t.Errorf("BatchUpdateStatuses: got %v", names)
	}
	if rev := batch.GetStatuses()[1]; rev.GetColor() != issuesv1.LabelColor_LABEL_COLOR_INFO || rev.GetWipLimit() != 2 {
		t.Errorf("estado nuevo: got %+v", rev)
	}
	moved, err := client.GetIssue(ctx, &issuesv1.GetIssueRequest{ProjectId: pid, Number: 3})
	if err != nil || moved.GetIssue().GetStatusId() != done.GetId() {
		t.Errorf("issue del estado borrado: got %+v, err %v", moved.GetIssue(), err)
	}
}

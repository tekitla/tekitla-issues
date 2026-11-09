package handler_test

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// Repos en memoria de la rebanada 3a (sin locks finos ni paginación). La
// lógica de verdad (ranks entre columnas, FK, carreras) la cubren el repo de
// Postgres y la prueba con grpcurl; acá alcanza con el recorrido.

func (s *memStore) addEvent(issueID, actor string, ev *model.IssueEvent) {
	ev.ID, ev.IssueID, ev.ActorUID, ev.CreatedAt = uuid.NewString(), issueID, actor, time.Now()
	s.events = append(s.events, ev)
}

func (s *memStore) projectStatuses(projectID string) []*model.Status {
	var out []*model.Status
	for _, st := range s.statuses {
		if st.ProjectID == projectID {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

func (s *memStore) status(projectID, id string) *model.Status {
	for _, st := range s.statuses {
		if st.ProjectID == projectID && st.ID == id {
			return st
		}
	}
	return nil
}

// --- IssueRepository (lo nuevo) ---

func (s memIssues) Move(_ context.Context, projectID string, number int32, target *model.Status, place port.Placement, actor string) (*model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.find(projectID, number)
	if it == nil {
		return nil, domainerrors.NotFoundError("issue not found", nil)
	}
	if !place.IsZero() {
		lo, hi := "", ""
		if n := s.find(projectID, place.AfterNumber); n != nil {
			lo = n.Rank
		}
		if n := s.find(projectID, place.BeforeNumber); n != nil {
			hi = n.Rank
		}
		rank, err := model.RankBetween(lo, hi)
		if err != nil {
			return nil, domainerrors.FailedPreconditionError("neighbors are out of order", nil).WithReason("STALE_ORDER")
		}
		it.Rank = rank
	}
	if it.StatusID != target.ID {
		from := s.status(projectID, it.StatusID)
		s.addEvent(it.ID, actor, &model.IssueEvent{Kind: model.EventStatusChanged, Status: model.NewStatusChange(from, target)})
		it.StatusID, it.State = target.ID, target.Category
		if target.Category != model.IssueStateCanceled {
			it.Discard = nil
		}
	}
	return it, nil
}

func (s memIssues) Update(_ context.Context, projectID string, number int32, patch model.IssuePatch, actor string) (*model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.find(projectID, number)
	if it == nil {
		return nil, domainerrors.NotFoundError("issue not found", nil)
	}
	labels := map[string]*model.Label{}
	for _, l := range s.labels {
		labels[l.ID] = l
	}
	for _, ev := range model.IssueChanges(it, patch, labels) {
		s.addEvent(it.ID, actor, ev)
	}
	if patch.Title != nil {
		it.Title = *patch.Title
	}
	if patch.Description != nil {
		it.Description = *patch.Description
	}
	if patch.Priority != nil {
		it.Priority = *patch.Priority
	}
	if patch.LabelIDs != nil {
		it.LabelIDs = append([]string{}, *patch.LabelIDs...)
	}
	return it, nil
}

func (s memIssues) AddComment(_ context.Context, projectID string, number int32, c *model.Comment) (*model.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.find(projectID, number)
	if it == nil {
		return nil, domainerrors.NotFoundError("issue not found", nil)
	}
	c.IssueID, c.CreatedAt = it.ID, time.Now()
	s.addEvent(it.ID, c.AuthorUID, &model.IssueEvent{Kind: model.EventCommented, Comment: c})
	return c, nil
}

func (s memIssues) ListEvents(_ context.Context, projectID string, number int32, _ port.PageFilter) (*port.EventPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.find(projectID, number)
	if it == nil {
		return nil, domainerrors.NotFoundError("issue not found", nil)
	}
	page := &port.EventPage{}
	for _, ev := range s.events {
		if ev.IssueID == it.ID {
			page.Events = append(page.Events, ev)
		}
	}
	return page, nil
}

// --- StatusRepository ---

func (s memStatuses) List(_ context.Context, projectID string) ([]*model.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projectStatuses(projectID), nil
}

func (s memStatuses) Get(_ context.Context, projectID, id string) (*model.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.status(projectID, id); st != nil {
		return st, nil
	}
	return nil, domainerrors.NotFoundError("status not found", nil)
}

func (s memStatuses) Create(_ context.Context, st *model.Status, afterID string) (*model.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.projectStatuses(st.ProjectID)
	for _, o := range list {
		if strings.EqualFold(o.Name, st.Name) {
			return nil, domainerrors.ConflictError("status name already exists", nil).WithReason("STATUS_NAME_TAKEN")
		}
	}
	lo, hi := list[len(list)-1].Rank, ""
	if afterID != "" {
		i := slices.IndexFunc(list, func(o *model.Status) bool { return o.ID == afterID })
		if i < 0 {
			return nil, domainerrors.ValidationError("after_status_id is not part of the project", nil)
		}
		lo = list[i].Rank
		if i+1 < len(list) {
			hi = list[i+1].Rank
		}
	}
	st.Rank, _ = model.RankBetween(lo, hi)
	s.statuses = append(s.statuses, st)
	return st, nil
}

func (s memStatuses) Update(_ context.Context, projectID, id string, patch port.StatusPatch) (*model.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status(projectID, id)
	if st == nil {
		return nil, domainerrors.NotFoundError("status not found", nil)
	}
	if patch.Name != nil {
		st.Name = *patch.Name
	}
	if patch.Color != nil {
		st.Color, _ = model.ResolveStatusColor(*patch.Color, st.Category)
	}
	if patch.WIPLimit != nil {
		st.WIPLimit = *patch.WIPLimit
	}
	if patch.Hidden != nil {
		st.Hidden = *patch.Hidden
	}
	return st, nil
}

func (s memStatuses) Delete(_ context.Context, projectID, id, moveToID, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status(projectID, id)
	if st == nil {
		return domainerrors.NotFoundError("status not found", nil)
	}
	same := 0
	for _, o := range s.projectStatuses(projectID) {
		if o.Category == st.Category {
			same++
		}
	}
	if same == 1 {
		return domainerrors.FailedPreconditionError("cannot delete the last status of its category", nil)
	}
	target := s.status(projectID, moveToID)
	for _, it := range s.issues {
		if it.StatusID != id {
			continue
		}
		if target == nil {
			return domainerrors.FailedPreconditionError("status has issues", nil)
		}
		s.addEvent(it.ID, actor, &model.IssueEvent{Kind: model.EventStatusChanged, Status: model.NewStatusChange(st, target)})
		it.StatusID, it.State = target.ID, target.Category
	}
	s.statuses = slices.DeleteFunc(s.statuses, func(o *model.Status) bool { return o.ID == id })
	return nil
}

// --- LabelRepository ---

func (s memLabels) List(_ context.Context, projectID string) ([]*model.Label, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*model.Label
	for _, l := range s.labels {
		if l.ProjectID == projectID {
			out = append(out, l)
		}
	}
	return out, nil
}

func (s memLabels) Create(_ context.Context, l *model.Label) (*model.Label, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.labels = append(s.labels, l)
	return l, nil
}

func (s memLabels) Update(_ context.Context, _, id string, name *string, color *model.LabelColor) (*model.Label, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.labels {
		if l.ID == id {
			if name != nil {
				l.Name = *name
			}
			if color != nil {
				l.Color = *color
			}
			return l, nil
		}
	}
	return nil, domainerrors.NotFoundError("label not found", nil)
}

func (s memLabels) Delete(_ context.Context, _, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.labels = slices.DeleteFunc(s.labels, func(l *model.Label) bool { return l.ID == id })
	return nil
}

// --- Recorrido de la rebanada 3a ---

func TestIssueHandler_WorkflowFlow(t *testing.T) {
	client := newClient(t)
	ctx := asUser(t, "u1")

	created, err := client.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "TK", Name: "Tekitla"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := created.GetProject().GetId()

	// Nace con un estado por categoría.
	list, err := client.ListStatuses(ctx, &issuesv1.ListStatusesRequest{ProjectId: pid})
	if err != nil || len(list.GetStatuses()) != 4 {
		t.Fatalf("ListStatuses: got %+v, err %v", list, err)
	}
	todo, doing := list.GetStatuses()[0], list.GetStatuses()[1]
	if todo.GetCategory() != issuesv1.IssueState_ISSUE_STATE_TODO || doing.GetName() != "En curso" {
		t.Errorf("default statuses: got %+v", list.GetStatuses())
	}

	review, err := client.CreateStatus(ctx, &issuesv1.CreateStatusRequest{
		ProjectId: pid, Name: "En revisión", Category: issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS, AfterStatusId: doing.GetId(),
	})
	if err != nil {
		t.Fatalf("CreateStatus: %v", err)
	}
	if r := review.GetStatus(); r.GetRank() <= doing.GetRank() {
		t.Errorf("CreateStatus: rank %q not after %q", r.GetRank(), doing.GetRank())
	}
	_, err = client.CreateStatus(ctx, &issuesv1.CreateStatusRequest{ProjectId: pid, Name: "en revisión", Category: issuesv1.IssueState_ISSUE_STATE_TODO})
	wantCode(t, "CreateStatus nombre repetido", err, codes.AlreadyExists)

	issue, err := client.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: "Columnas"})
	if err != nil || issue.GetIssue().GetStatusId() != todo.GetId() {
		t.Fatalf("CreateIssue: got %+v, err %v", issue.GetIssue(), err)
	}

	moved, err := client.MoveIssue(ctx, &issuesv1.MoveIssueRequest{ProjectId: pid, Number: 1, StatusId: review.GetStatus().GetId()})
	if err != nil || moved.GetIssue().GetState() != issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS || moved.GetIssue().GetStatusId() != review.GetStatus().GetId() {
		t.Fatalf("MoveIssue status_id: got %+v, err %v", moved.GetIssue(), err)
	}

	label, err := client.CreateLabel(ctx, &issuesv1.CreateLabelRequest{ProjectId: pid, Name: "Frontend", Color: issuesv1.LabelColor_LABEL_COLOR_INFO})
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	title := "Columnas desde los datos"
	updated, err := client.UpdateIssue(ctx, &issuesv1.UpdateIssueRequest{
		ProjectId: pid, Number: 1, Title: &title,
		Labels: &issuesv1.LabelIds{Ids: []string{label.GetLabel().GetId()}},
	})
	if err != nil || updated.GetIssue().GetTitle() != title || !slices.Equal(updated.GetIssue().GetLabelIds(), []string{label.GetLabel().GetId()}) {
		t.Fatalf("UpdateIssue: got %+v, err %v", updated.GetIssue(), err)
	}

	comment, err := client.CreateComment(ctx, &issuesv1.CreateCommentRequest{ProjectId: pid, Number: 1, Body: "Listo"})
	if err != nil || comment.GetComment().GetAuthorId() != "u1" {
		t.Fatalf("CreateComment: got %+v, err %v", comment, err)
	}

	// Borrar «En revisión» pasa el issue a «En curso».
	if _, err := client.DeleteStatus(ctx, &issuesv1.DeleteStatusRequest{ProjectId: pid, StatusId: review.GetStatus().GetId(), MoveToStatusId: doing.GetId()}); err != nil {
		t.Fatalf("DeleteStatus: %v", err)
	}
	_, err = client.DeleteStatus(ctx, &issuesv1.DeleteStatusRequest{ProjectId: pid, StatusId: todo.GetId()})
	wantCode(t, "DeleteStatus último de su categoría", err, codes.FailedPrecondition)

	events, err := client.ListIssueEvents(ctx, &issuesv1.ListIssueEventsRequest{ProjectId: pid, Number: 1})
	if err != nil {
		t.Fatalf("ListIssueEvents: %v", err)
	}
	var kinds []string
	for _, ev := range events.GetEvents() {
		switch c := ev.GetChange().(type) {
		case *issuesv1.IssueEvent_Created:
			kinds = append(kinds, "created")
		case *issuesv1.IssueEvent_StatusChanged:
			kinds = append(kinds, "status:"+c.StatusChanged.GetToStatusName())
		case *issuesv1.IssueEvent_TitleChanged:
			kinds = append(kinds, "title")
		case *issuesv1.IssueEvent_LabelsChanged:
			kinds = append(kinds, "labels:"+strings.Join(c.LabelsChanged.GetAddedLabelNames(), ","))
		case *issuesv1.IssueEvent_Commented:
			kinds = append(kinds, "comment:"+c.Commented.GetBody())
		default:
			kinds = append(kinds, "?")
		}
		if ev.GetActorId() != "u1" {
			t.Errorf("event actor: got %q", ev.GetActorId())
		}
	}
	want := []string{"created", "status:En revisión", "title", "labels:Frontend", "comment:Listo", "status:En curso"}
	if !slices.Equal(kinds, want) {
		t.Errorf("events: got %v, want %v", kinds, want)
	}

	// Otro usuario no ve nada del proyecto.
	other := asUser(t, "u2")
	_, err = client.ListStatuses(other, &issuesv1.ListStatusesRequest{ProjectId: pid})
	wantCode(t, "ListStatuses ajeno", err, codes.NotFound)
	_, err = client.ListIssueEvents(other, &issuesv1.ListIssueEventsRequest{ProjectId: pid, Number: 1})
	wantCode(t, "ListIssueEvents ajeno", err, codes.NotFound)

	// Validaciones que llegan como INVALID_ARGUMENT.
	_, err = client.MoveIssue(ctx, &issuesv1.MoveIssueRequest{ProjectId: pid, Number: 1, StatusId: uuid.NewString()})
	wantCode(t, "MoveIssue estado de otro proyecto", err, codes.InvalidArgument)
	_, err = client.UpdateIssue(ctx, &issuesv1.UpdateIssueRequest{ProjectId: pid, Number: 1, Labels: &issuesv1.LabelIds{Ids: []string{uuid.NewString()}}})
	wantCode(t, "UpdateIssue etiqueta ajena", err, codes.InvalidArgument)
	_, err = client.CreateComment(ctx, &issuesv1.CreateCommentRequest{ProjectId: pid, Number: 1, Body: " "})
	wantCode(t, "CreateComment vacío", err, codes.InvalidArgument)
}

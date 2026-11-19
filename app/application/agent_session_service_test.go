package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

type mockSessionRepo struct {
	projectIDsFn    func(ctx context.Context, workspaceIDs, keys []string) (map[string]string, error)
	findAgentFn     func(ctx context.Context, ownerID string, tool model.AgentTool, name string) (*model.Actor, error)
	reportFn        func(ctx context.Context, key model.SessionKey, now time.Time, plan func([]*model.AgentSession) model.ReportOutcome) (model.ReportOutcome, error)
	listFn          func(ctx context.Context, f port.AgentSessionFilter) (*port.AgentSessionPage, error)
	listForIssuesFn func(ctx context.Context, projectID string, numbers []int32, since time.Time) ([]*model.AgentSession, error)
}

var _ port.AgentSessionRepository = (*mockSessionRepo)(nil)

func (m *mockSessionRepo) ProjectIDsByKey(ctx context.Context, ws, keys []string) (map[string]string, error) {
	if m.projectIDsFn == nil {
		return map[string]string{}, nil
	}
	return m.projectIDsFn(ctx, ws, keys)
}

func (m *mockSessionRepo) FindAgent(ctx context.Context, ownerID string, tool model.AgentTool, name string) (*model.Actor, error) {
	if m.findAgentFn == nil {
		return nil, domainerrors.NotFoundError("agent not found", nil)
	}
	return m.findAgentFn(ctx, ownerID, tool, name)
}

func (m *mockSessionRepo) Report(ctx context.Context, key model.SessionKey, now time.Time, plan func([]*model.AgentSession) model.ReportOutcome) (model.ReportOutcome, error) {
	if m.reportFn == nil {
		return plan(nil), nil
	}
	return m.reportFn(ctx, key, now, plan)
}

func (m *mockSessionRepo) List(ctx context.Context, f port.AgentSessionFilter) (*port.AgentSessionPage, error) {
	if m.listFn == nil {
		return &port.AgentSessionPage{}, nil
	}
	return m.listFn(ctx, f)
}

func (m *mockSessionRepo) ListForIssues(ctx context.Context, projectID string, numbers []int32, since time.Time) ([]*model.AgentSession, error) {
	if m.listForIssuesFn == nil {
		return nil, nil
	}
	return m.listForIssuesFn(ctx, projectID, numbers, since)
}

func sessionSvc(actors *mockActorRepo, repo *mockSessionRepo) *application.AgentSessionService {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ws := &mockWorkspaceRepo{getPersonalFn: func(context.Context, string) (*model.Workspace, error) {
		return &model.Workspace{ID: "ws1", OwnerUID: "u1"}, nil
	}}
	if actors.ensurePersonFn == nil {
		actors.ensurePersonFn = func(_ context.Context, p *model.Actor) (*model.Actor, error) { return p, nil }
	}
	return application.NewAgentSessionService(ws, actors, repo, 0).WithClock(func() time.Time { return now })
}

func oneSession(tool model.AgentTool) application.ReportInput {
	return application.ReportInput{Source: model.SessionSourceHerdr, Machine: "PC-01", Sessions: []model.ReportedSession{
		{ExternalID: "s1", Tool: tool, Link: model.SessionLinkCwd, ProjectKey: "tk", IssueNumber: 26, Status: model.SessionWorking},
	}}
}

func TestAgentSessionService_Report_CreatesAgentAndResolvesProject(t *testing.T) {
	var created *model.Actor
	actors := &mockActorRepo{createAgentFn: func(_ context.Context, a *model.Actor) (*model.Actor, error) { created = a; return a, nil }}
	var planned model.ReportOutcome
	repo := &mockSessionRepo{
		projectIDsFn: func(_ context.Context, ws, keys []string) (map[string]string, error) {
			if len(ws) != 1 || ws[0] != "ws1" || len(keys) != 1 || keys[0] != "TK" {
				t.Errorf("ProjectIDsByKey(%v, %v)", ws, keys)
			}
			return map[string]string{"TK": "p-tk"}, nil
		},
		reportFn: func(_ context.Context, key model.SessionKey, _ time.Time, plan func([]*model.AgentSession) model.ReportOutcome) (model.ReportOutcome, error) {
			if key.Machine != "PC-01" || key.Source != model.SessionSourceHerdr {
				t.Errorf("key: %+v", key)
			}
			planned = plan(nil)
			return planned, nil
		},
	}
	res, err := sessionSvc(actors, repo).Report(context.Background(), "u1", oneSession(model.AgentToolClaudeCode))
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if created == nil || created.DisplayName != "Claude Code PC-01" || created.Tool != model.AgentToolClaudeCode {
		t.Fatalf("agent: %+v", created)
	}
	if res.Created != 1 || len(res.Rejected) != 0 || planned.Upserts[0].ProjectID != "p-tk" || planned.Upserts[0].AgentActorID != created.ID {
		t.Errorf("result %+v, planned %+v", res, planned.Upserts[0])
	}
}

func TestAgentSessionService_Report_Rejections(t *testing.T) {
	ctx := context.Background()

	// Agente revocado: la sesión no se guarda.
	revoked := time.Now()
	repo := &mockSessionRepo{findAgentFn: func(context.Context, string, model.AgentTool, string) (*model.Actor, error) {
		return &model.Actor{ID: "ag", RevokedAt: &revoked}, nil
	}}
	res, err := sessionSvc(&mockActorRepo{}, repo).Report(ctx, "u1", oneSession(model.AgentToolCodex))
	if err != nil || res.Created != 0 || len(res.Rejected) != 2 || res.Rejected[1].Saved {
		// 2: proyecto desconocido (guardada) + agente revocado (no guardada).
		t.Errorf("revoked: %+v, %v", res, err)
	}

	// Nombre tomado por un agente de otra herramienta: dos intentos y rechazo.
	calls := 0
	actors := &mockActorRepo{createAgentFn: func(context.Context, *model.Actor) (*model.Actor, error) {
		calls++
		return nil, domainerrors.ConflictError("agent name already exists", nil)
	}}
	res, err = sessionSvc(actors, &mockSessionRepo{}).Report(ctx, "u1", oneSession(model.AgentToolCodex))
	if err != nil || calls != 2 || res.Created != 0 || len(res.Rejected) != 2 {
		t.Errorf("name taken: %+v, %v (calls %d)", res, err, calls)
	}

	// Persona revocada: FailedPrecondition.
	actors = &mockActorRepo{ensurePersonFn: func(_ context.Context, p *model.Actor) (*model.Actor, error) {
		p.RevokedAt = &revoked
		return p, nil
	}}
	_, err = sessionSvc(actors, &mockSessionRepo{}).Report(ctx, "u1", oneSession(model.AgentToolCodex))
	if !domainerrors.IsFailedPrecondition(err) {
		t.Errorf("revoked person: %v", err)
	}
}

func TestAgentSessionService_Report_PropagatesRepoErrors(t *testing.T) {
	boom := domainerrors.InternalError("boom", nil)
	ctx := context.Background()
	cases := map[string]*mockSessionRepo{
		"projects": {projectIDsFn: func(context.Context, []string, []string) (map[string]string, error) { return nil, boom }},
		"agent":    {findAgentFn: func(context.Context, string, model.AgentTool, string) (*model.Actor, error) { return nil, boom }},
		"report": {reportFn: func(context.Context, model.SessionKey, time.Time, func([]*model.AgentSession) model.ReportOutcome) (model.ReportOutcome, error) {
			return model.ReportOutcome{}, boom
		}},
	}
	for name, repo := range cases {
		actors := &mockActorRepo{createAgentFn: func(_ context.Context, a *model.Actor) (*model.Actor, error) { return a, nil }}
		if _, err := sessionSvc(actors, repo).Report(ctx, "u1", oneSession(model.AgentToolCodex)); !errors.Is(err, boom) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := sessionSvc(&mockActorRepo{}, &mockSessionRepo{}).Report(ctx, "", oneSession(model.AgentToolCodex)); err == nil {
		t.Error("empty uid: want error")
	}
	if _, err := sessionSvc(&mockActorRepo{}, &mockSessionRepo{}).Report(ctx, "u1", application.ReportInput{Source: model.SessionSourceHerdr, Machine: "a b"}); !domainerrors.IsValidation(err) {
		t.Errorf("bad machine: %v", err)
	}
}

func TestAgentSessionService_List_Filters(t *testing.T) {
	var got port.AgentSessionFilter
	repo := &mockSessionRepo{listFn: func(_ context.Context, f port.AgentSessionFilter) (*port.AgentSessionPage, error) {
		got = f
		return &port.AgentSessionPage{}, nil
	}}
	svc := sessionSvc(&mockActorRepo{}, repo)
	_, err := svc.List(context.Background(), "u1", application.ListSessionsInput{
		ProjectID: "6F9619FF-8B86-D011-B42D-00CF4FC964FF", IssueNumber: 26,
		Statuses: []model.SessionStatus{model.SessionStalled, model.SessionIdle},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.ProjectID != "6f9619ff-8b86-d011-b42d-00cf4fc964ff" || got.IssueNumber != 26 || got.ActiveSince.IsZero() ||
		!got.Stalled || got.Working || len(got.Statuses) != 1 || got.Statuses[0] != model.SessionIdle ||
		!got.StallBefore.Equal(got.ActiveSince.Add(model.SessionActiveWindow-model.DefaultSessionStallAfter)) || got.Page.Limit != application.DefaultPageLimit {
		t.Errorf("filter: %+v", got)
	}
	bad := []application.ListSessionsInput{
		{ProjectID: "nope"},
		{Unlinked: true, ProjectID: "6f9619ff-8b86-d011-b42d-00cf4fc964ff"},
		{Statuses: []model.SessionStatus{"bogus"}},
		{AgentActorID: "x"},
	}
	for i, in := range bad {
		if _, err := svc.List(context.Background(), "u1", in); !domainerrors.IsValidation(err) {
			t.Errorf("bad %d: %v", i, err)
		}
	}
}

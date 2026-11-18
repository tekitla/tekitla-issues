package handler_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// Repo de sesiones en memoria (issue 026). El advisory lock, el upsert y el
// JOIN del nombre del agente los cubren el repo de Postgres y la prueba con
// grpcurl.

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type memSessions struct {
	store *memStore
	mu    sync.Mutex
	rows  []*model.AgentSession
}

var _ port.AgentSessionRepository = (*memSessions)(nil)

func (m *memSessions) ProjectIDsByKey(_ context.Context, workspaceIDs []string, keys []string) (map[string]string, error) {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	out := map[string]string{}
	for _, p := range m.store.projects {
		if slices.Contains(workspaceIDs, p.WorkspaceID) && slices.Contains(keys, p.Key) {
			out[p.Key] = p.ID
		}
	}
	return out, nil
}

func (m *memSessions) FindAgent(_ context.Context, ownerID string, tool model.AgentTool, name string) (*model.Actor, error) {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	var found *model.Actor
	for _, a := range m.store.actors {
		if a.OwnerActorID == ownerID && a.Tool == tool && strings.EqualFold(a.DisplayName, name) {
			if found == nil || (found.RevokedAt != nil && a.RevokedAt == nil) {
				found = a
			}
		}
	}
	if found == nil {
		return nil, domainerrors.NotFoundError("agent not found", nil)
	}
	return found, nil
}

func (m *memSessions) withName(s *model.AgentSession) *model.AgentSession {
	c := *s
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	for _, a := range m.store.actors {
		if a.ID == s.AgentActorID {
			c.AgentDisplayName = a.DisplayName
		}
	}
	return &c
}

func (m *memSessions) Report(_ context.Context, key model.SessionKey, now time.Time, plan func([]*model.AgentSession) model.ReportOutcome) (model.ReportOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var existing []*model.AgentSession
	for _, s := range m.rows {
		if s.OwnerActorID == key.OwnerActorID && s.Source == key.Source && s.Machine == key.Machine {
			c := *s
			existing = append(existing, &c)
		}
	}
	out := plan(existing)
	for _, u := range out.Upserts {
		c := *u
		replaced := false
		for i, s := range m.rows {
			if s.ID == u.ID {
				m.rows[i], replaced = &c, true
			}
		}
		if !replaced {
			m.rows = append(m.rows, &c)
		}
	}
	for _, s := range m.rows {
		if slices.Contains(out.EndedIDs, s.ID) {
			t := now
			s.Status, s.EndedAt, s.StatusChangedAt = model.SessionEnded, &t, now
			s.WaitingOn, s.WaitingTarget, s.WaitingText = model.WaitingNone, "", ""
		}
	}
	return out, nil
}

func (m *memSessions) List(_ context.Context, f port.AgentSessionFilter) (*port.AgentSessionPage, error) {
	m.mu.Lock()
	var out []*model.AgentSession
	for _, s := range m.rows {
		switch {
		case s.OwnerActorID != f.OwnerActorID,
			f.ProjectID != "" && s.ProjectID != f.ProjectID,
			f.IssueNumber > 0 && s.IssueNumber != f.IssueNumber,
			f.Unlinked && s.ProjectID != "",
			f.AgentActorID != "" && s.AgentActorID != f.AgentActorID,
			len(f.StoredStatuses) > 0 && !slices.Contains(f.StoredStatuses, s.Status),
			!f.ActiveSince.IsZero() && (s.Status == model.SessionEnded || s.LastSeenAt.Before(f.ActiveSince)):
			continue
		}
		out = append(out, s)
	}
	m.mu.Unlock()
	for i, s := range out {
		out[i] = m.withName(s)
	}
	return &port.AgentSessionPage{Sessions: out}, nil
}

func (m *memSessions) ListForIssues(_ context.Context, projectID string, numbers []int32, activeSince time.Time) ([]*model.AgentSession, error) {
	m.mu.Lock()
	var out []*model.AgentSession
	for _, s := range m.rows {
		if s.ProjectID == projectID && slices.Contains(numbers, s.IssueNumber) && s.Status != model.SessionEnded && !s.LastSeenAt.Before(activeSince) {
			out = append(out, s)
		}
	}
	m.mu.Unlock()
	for i, s := range out {
		out[i] = m.withName(s)
	}
	return out, nil
}

// reported arma una sesión del reporte ligada a key-n (key vacía = sin
// issue).
func reported(ext, key string, n int32, link issuesv1.SessionLink, st issuesv1.SessionStatus) *issuesv1.ReportedSession {
	return &issuesv1.ReportedSession{
		ExternalId: ext, Tool: issuesv1.AgentTool_AGENT_TOOL_CLAUDE_CODE,
		ProjectKey: key, IssueNumber: n, Link: link, Status: st,
	}
}

func report(t *testing.T, c clients, ctx context.Context, machine string, sessions ...*issuesv1.ReportedSession) *issuesv1.ReportSessionsResponse {
	t.Helper()
	res, err := c.sessions.ReportSessions(ctx, &issuesv1.ReportSessionsRequest{
		Source: issuesv1.SessionSource_SESSION_SOURCE_HERDR, Machine: machine, Sessions: sessions,
	})
	if err != nil {
		t.Fatalf("ReportSessions: %v", err)
	}
	return res
}

func onlySession(t *testing.T, c clients, ctx context.Context, req *issuesv1.ListAgentSessionsRequest) *issuesv1.AgentSession {
	t.Helper()
	res, err := c.sessions.ListAgentSessions(ctx, req)
	if err != nil {
		t.Fatalf("ListAgentSessions: %v", err)
	}
	if len(res.GetSessions()) != 1 {
		t.Fatalf("ListAgentSessions: got %d sessions, want 1: %v", len(res.GetSessions()), res.GetSessions())
	}
	return res.GetSessions()[0]
}

const (
	working = issuesv1.SessionStatus_SESSION_STATUS_WORKING
	waiting = issuesv1.SessionStatus_SESSION_STATUS_WAITING_OK
	byCwd   = issuesv1.SessionLink_SESSION_LINK_CWD
	byName  = issuesv1.SessionLink_SESSION_LINK_AGENT_NAME
	noLink  = issuesv1.SessionLink_SESSION_LINK_NONE
)

func TestAgentSessions_ReportAndList(t *testing.T) {
	c := newClients(t)
	ctx := asUser(t, "u1")
	proj, err := c.issues.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "RND", Name: "Round"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := proj.GetProject().GetId()

	// CU-026-1: ligada a RND-231 por el nombre, con cwd relativo.
	s1 := reported("s1", "rnd", 231, byName, working)
	s1.CwdRel = "round/worktrees/issue-231/front"
	s1.SessionName = "round-issue-231"
	// CU-026-3: sin issue, en la raíz.
	s3 := reported("s3", "", 0, noLink, issuesv1.SessionStatus_SESSION_STATUS_IDLE)
	s3.CwdRel = "."
	res := report(t, c, ctx, "PC-01", s1, s3)
	if res.GetCreated() != 2 || len(res.GetRejected()) != 0 {
		t.Fatalf("first report: %+v", res)
	}

	got := onlySession(t, c, ctx, &issuesv1.ListAgentSessionsRequest{ProjectId: pid, IssueNumber: 231})
	if got.GetStatus() != working || got.GetCwdRel() != "round/worktrees/issue-231/front" || got.GetLink() != byName ||
		got.GetAgentDisplayName() != "Claude Code PC-01" || got.GetMachine() != "PC-01" || got.GetSessionName() != "round-issue-231" {
		t.Errorf("CU-026-1: got %+v", got)
	}
	agentID := got.GetAgentActorId()
	if un := onlySession(t, c, ctx, &issuesv1.ListAgentSessionsRequest{Unlinked: true}); un.GetExternalId() != "s3" || un.GetProjectId() != "" || un.GetCwdRel() != "." {
		t.Errorf("CU-026-3: got %+v", un)
	}

	// CU-026-7: el mismo reporte otra vez no cambia nada.
	again := report(t, c, ctx, "PC-01", s1, s3)
	if again.GetCreated() != 0 || again.GetUpdated() != 0 || again.GetUnchanged() != 2 || again.GetEnded() != 0 {
		t.Errorf("CU-026-7: got %+v", again)
	}
	// Un solo agente por herramienta y máquina.
	actors, _ := c.actors.ListActors(ctx, &issuesv1.ListActorsRequest{})
	if n := len(actors.GetActors()); n != 2 {
		t.Errorf("actors: got %d (person + 1 agent expected)", n)
	}

	// CU-026-4 y CU-026-11: pasa a esperando-ok por la persona.
	c.clock.advance(time.Minute)
	s1w := reported("s1", "RND", 231, byName, waiting)
	s1w.CwdRel = s1.CwdRel
	s1w.WaitingOn = issuesv1.WaitingOn_WAITING_ON_PERSON
	s1w.WaitingText = "🙋 pregunta"
	before := got.GetStatusChangedAt().AsTime()
	report(t, c, ctx, "PC-01", s1w, s3)
	got = onlySession(t, c, ctx, &issuesv1.ListAgentSessionsRequest{ProjectId: pid, IssueNumber: 231})
	if got.GetStatus() != waiting || got.GetWaitingOn() != issuesv1.WaitingOn_WAITING_ON_PERSON || !got.GetStatusChangedAt().AsTime().After(before) {
		t.Errorf("CU-026-4/11: got %+v", got)
	}

	// Resumen en GetIssue (con lista) y en ListIssues (solo contadores).
	for i := 0; i < 231; i++ {
		// RND-231 tiene que existir para GetIssue.
		if _, err := c.issues.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: "x"}); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
	}
	gi, err := c.issues.GetIssue(ctx, &issuesv1.GetIssueRequest{ProjectId: pid, Number: 231})
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	sum := gi.GetIssue().GetSessions()
	if sum.GetActive() != 1 || sum.GetWaiting() != 1 || !sum.GetNeedsPerson() || len(sum.GetItems()) != 1 ||
		sum.GetItems()[0].GetAgentDisplayName() != "Claude Code PC-01" || sum.GetItems()[0].GetWaitingOn() != issuesv1.WaitingOn_WAITING_ON_PERSON {
		t.Errorf("GetIssue sessions: got %+v", sum)
	}
	li, err := c.issues.ListIssues(ctx, &issuesv1.ListIssuesRequest{ProjectId: pid, Query: "RND-231"})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	for _, is := range li.GetIssues() {
		switch {
		case is.GetNumber() == 231 && (is.GetSessions().GetActive() != 1 || len(is.GetSessions().GetItems()) != 0):
			t.Errorf("ListIssues 231 sessions: got %+v", is.GetSessions())
		case is.GetNumber() != 231 && is.GetSessions() != nil:
			t.Errorf("ListIssues %d: sessions should be absent", is.GetNumber())
		}
	}

	// CU-026-12: trabajando con un token de espera viejo → NONE.
	s1old := reported("s1", "RND", 231, byName, working)
	s1old.CwdRel = s1.CwdRel
	s1old.WaitingOn = issuesv1.WaitingOn_WAITING_ON_SESSION
	s1old.WaitingTarget = "tk-worktrees"
	report(t, c, ctx, "PC-01", s1old, s3)
	got = onlySession(t, c, ctx, &issuesv1.ListAgentSessionsRequest{ProjectId: pid, IssueNumber: 231})
	if got.GetWaitingOn() != issuesv1.WaitingOn_WAITING_ON_NONE || got.GetWaitingTarget() != "" {
		t.Errorf("CU-026-12: got %+v", got)
	}

	// CU-026-5: trabajando sin reportes durante más de 10 minutos → colgado.
	c.clock.advance(11 * time.Minute)
	got = onlySession(t, c, ctx, &issuesv1.ListAgentSessionsRequest{ProjectId: pid, IssueNumber: 231})
	if got.GetStatus() != issuesv1.SessionStatus_SESSION_STATUS_STALLED {
		t.Errorf("CU-026-5: got %v", got.GetStatus())
	}
	gi, _ = c.issues.GetIssue(ctx, &issuesv1.GetIssueRequest{ProjectId: pid, Number: 231})
	if gi.GetIssue().GetSessions().GetStalled() != 1 {
		t.Errorf("CU-026-5 summary: got %+v", gi.GetIssue().GetSessions())
	}
	stalled, _ := c.sessions.ListAgentSessions(ctx, &issuesv1.ListAgentSessionsRequest{Statuses: []issuesv1.SessionStatus{working}})
	if len(stalled.GetSessions()) != 0 {
		t.Errorf("WORKING filter should drop stalled: %v", stalled.GetSessions())
	}

	// CU-026-6: el panel se cierra → terminada; otra máquina no se toca.
	report(t, c, ctx, "PC-02", reported("other", "", 0, noLink, working))
	end := report(t, c, ctx, "PC-01", s3)
	if end.GetEnded() != 1 {
		t.Errorf("CU-026-6: got %+v", end)
	}
	all, _ := c.sessions.ListAgentSessions(ctx, &issuesv1.ListAgentSessionsRequest{IncludeInactive: true, ProjectId: pid})
	if len(all.GetSessions()) != 1 || all.GetSessions()[0].GetStatus() != issuesv1.SessionStatus_SESSION_STATUS_ENDED || all.GetSessions()[0].GetEndedAt() == nil {
		t.Errorf("CU-026-6 list: got %v", all.GetSessions())
	}
	active, _ := c.sessions.ListAgentSessions(ctx, &issuesv1.ListAgentSessionsRequest{})
	if len(active.GetSessions()) != 2 {
		t.Errorf("active after end: got %d, want 2 (s3 and PC-02)", len(active.GetSessions()))
	}
	gi, _ = c.issues.GetIssue(ctx, &issuesv1.GetIssueRequest{ProjectId: pid, Number: 231})
	if gi.GetIssue().GetSessions() != nil {
		t.Errorf("summary after end: got %+v", gi.GetIssue().GetSessions())
	}
	// Filtro por agente.
	byAgent, _ := c.sessions.ListAgentSessions(ctx, &issuesv1.ListAgentSessionsRequest{AgentActorId: agentID})
	if len(byAgent.GetSessions()) != 1 {
		t.Errorf("agent filter: got %d", len(byAgent.GetSessions()))
	}

	// Más de 24 h sin reporte: deja de ser activa.
	c.clock.advance(25 * time.Hour)
	active, _ = c.sessions.ListAgentSessions(ctx, &issuesv1.ListAgentSessionsRequest{})
	if len(active.GetSessions()) != 0 {
		t.Errorf("24h window: got %d active", len(active.GetSessions()))
	}
}

func TestAgentSessions_UnknownProjectAndValidation(t *testing.T) {
	c := newClients(t)
	ctx := asUser(t, "u1")

	// Clave desconocida: se guarda sin issue y se anota.
	res := report(t, c, ctx, "PC-01", reported("s1", "XYZ", 4, byCwd, working))
	if res.GetCreated() != 1 || len(res.GetRejected()) != 1 || !res.GetRejected()[0].GetSaved() {
		t.Fatalf("unknown project: %+v", res)
	}
	if got := onlySession(t, c, ctx, &issuesv1.ListAgentSessionsRequest{Unlinked: true}); got.GetLink() != noLink || got.GetIssueNumber() != 0 {
		t.Errorf("unknown project stored as: %+v", got)
	}

	bad := map[string]*issuesv1.ReportSessionsRequest{
		// CU-026-9: nada de rutas absolutas.
		"absolute cwd": {Source: issuesv1.SessionSource_SESSION_SOURCE_HERDR, Machine: "PC-01",
			Sessions: []*issuesv1.ReportedSession{{ExternalId: "a", Tool: issuesv1.AgentTool_AGENT_TOOL_CODEX, CwdRel: `C:\Users\victor\dev`, Link: noLink, Status: working}}},
		"dotdot cwd": {Source: issuesv1.SessionSource_SESSION_SOURCE_HERDR, Machine: "PC-01",
			Sessions: []*issuesv1.ReportedSession{{ExternalId: "a", Tool: issuesv1.AgentTool_AGENT_TOOL_CODEX, CwdRel: "a/../../etc", Link: noLink, Status: working}}},
		"stalled reported": {Source: issuesv1.SessionSource_SESSION_SOURCE_HERDR, Machine: "PC-01",
			Sessions: []*issuesv1.ReportedSession{{ExternalId: "a", Tool: issuesv1.AgentTool_AGENT_TOOL_CODEX, Link: noLink, Status: issuesv1.SessionStatus_SESSION_STATUS_STALLED}}},
		"bad machine":    {Source: issuesv1.SessionSource_SESSION_SOURCE_HERDR, Machine: "PC 01"},
		"no source":      {Machine: "PC-01"},
		"duplicate":      {Source: issuesv1.SessionSource_SESSION_SOURCE_HERDR, Machine: "PC-01", Sessions: []*issuesv1.ReportedSession{reported("d", "", 0, noLink, working), reported("d", "", 0, noLink, working)}},
		"link mismatch":  {Source: issuesv1.SessionSource_SESSION_SOURCE_HERDR, Machine: "PC-01", Sessions: []*issuesv1.ReportedSession{reported("d", "RND", 1, noLink, working)}},
		"number no key":  {Source: issuesv1.SessionSource_SESSION_SOURCE_HERDR, Machine: "PC-01", Sessions: []*issuesv1.ReportedSession{reported("d", "", 3, noLink, working)}},
		"no tool":        {Source: issuesv1.SessionSource_SESSION_SOURCE_HERDR, Machine: "PC-01", Sessions: []*issuesv1.ReportedSession{{ExternalId: "a", Link: noLink, Status: working}}},
		"empty external": {Source: issuesv1.SessionSource_SESSION_SOURCE_HERDR, Machine: "PC-01", Sessions: []*issuesv1.ReportedSession{reported("", "", 0, noLink, working)}},
	}
	for name, req := range bad {
		_, err := c.sessions.ReportSessions(ctx, req)
		wantCode(t, name, err, codes.InvalidArgument)
	}
	// Un reporte inválido no tocó nada: s1 sigue activa.
	if got := onlySession(t, c, ctx, &issuesv1.ListAgentSessionsRequest{}); got.GetExternalId() != "s1" {
		t.Errorf("after invalid reports: %+v", got)
	}

	_, err := c.sessions.ListAgentSessions(ctx, &issuesv1.ListAgentSessionsRequest{IssueNumber: 3})
	wantCode(t, "issue without project", err, codes.InvalidArgument)
	_, err = c.sessions.ListAgentSessions(ctx, &issuesv1.ListAgentSessionsRequest{Statuses: []issuesv1.SessionStatus{issuesv1.SessionStatus_SESSION_STATUS_ENDED}})
	wantCode(t, "ended without include_inactive", err, codes.InvalidArgument)
}

func TestAgentSessions_Identity(t *testing.T) {
	c := newClients(t)
	req := &issuesv1.ReportSessionsRequest{Source: issuesv1.SessionSource_SESSION_SOURCE_HERDR, Machine: "PC-01"}

	_, err := c.sessions.ReportSessions(asUser(t, ""), req)
	wantCode(t, "no identity", err, codes.Unauthenticated)

	// CU-026-8 (parte del servicio): un token de agente no reporta. El 403
	// por el permiso sessions:write lo da el gateway.
	agentCtx := metadata.AppendToOutgoingContext(asUser(t, "u1"), "x-agent-id", "8f0c2a4e-8d7a-4b55-9d61-2f3e0f1b6c11")
	_, err = c.sessions.ReportSessions(agentCtx, req)
	wantCode(t, "agent token", err, codes.PermissionDenied)

	// Las sesiones de otra persona no se ven.
	report(t, c, asUser(t, "u1"), "PC-01", reported("s1", "", 0, noLink, working))
	res, err := c.sessions.ListAgentSessions(asUser(t, "u2"), &issuesv1.ListAgentSessionsRequest{})
	if err != nil || len(res.GetSessions()) != 0 {
		t.Errorf("other user: got %v, %v", res.GetSessions(), err)
	}
}

func TestAgentSessions_RevokedAgent(t *testing.T) {
	c := newClients(t)
	ctx := asUser(t, "u1")
	report(t, c, ctx, "PC-01", reported("s1", "", 0, noLink, working))
	got := onlySession(t, c, ctx, &issuesv1.ListAgentSessionsRequest{})
	if _, err := c.actors.RevokeActor(ctx, &issuesv1.RevokeActorRequest{ActorId: got.GetAgentActorId()}); err != nil {
		t.Fatalf("RevokeActor: %v", err)
	}
	res := report(t, c, ctx, "PC-01", reported("s2", "", 0, noLink, working))
	if res.GetCreated() != 0 || len(res.GetRejected()) != 1 || res.GetRejected()[0].GetSaved() {
		t.Errorf("revoked agent: %+v", res)
	}
	// Victor crea otro con el mismo nombre desde el panel: vuelve a reportar.
	if _, err := c.actors.CreateAgent(ctx, &issuesv1.CreateAgentRequest{DisplayName: "Claude Code PC-01", Tool: issuesv1.AgentTool_AGENT_TOOL_CLAUDE_CODE}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if res := report(t, c, ctx, "PC-01", reported("s2", "", 0, noLink, working)); res.GetCreated() != 1 || len(res.GetRejected()) != 0 {
		t.Errorf("after new agent: %+v", res)
	}
}

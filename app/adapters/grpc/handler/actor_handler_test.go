package handler_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// Repos en memoria del issue 025 (actores, tokens e importación). Como en
// la 3a, la lógica de verdad (cascada en SQL, ON CONFLICT, locks) la cubren
// el repo de Postgres y la prueba con grpcurl.

type memToken struct {
	token *model.Token
	hash  []byte
}

type memActors struct{ *memStore }
type memTokens struct{ *memStore }

var (
	_ port.ActorRepository = memActors{}
	_ port.TokenRepository = memTokens{}
)

func (s memActors) EnsurePerson(_ context.Context, person *model.Actor) (*model.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.actors {
		if a.UserUID == person.UserUID {
			return a, nil
		}
	}
	p := *person
	s.actors = append(s.actors, &p)
	return &p, nil
}

func (s memActors) get(id string) *model.Actor {
	for _, a := range s.actors {
		if a.ID == id {
			return a
		}
	}
	return nil
}

func (s memActors) Get(_ context.Context, id string) (*model.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.get(id); a != nil {
		return a, nil
	}
	return nil, domainerrors.NotFoundError("actor not found", nil)
}

func (s memActors) ListAgents(_ context.Context, ownerID string, includeRevoked bool) ([]*model.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*model.Actor
	for _, a := range s.actors {
		if a.OwnerActorID == ownerID && (includeRevoked || a.RevokedAt == nil) {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b *model.Actor) int {
		return strings.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName))
	})
	return out, nil
}

func (s memActors) CreateAgent(_ context.Context, agent *model.Actor) (*model.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.actors {
		if a.OwnerActorID == agent.OwnerActorID && a.RevokedAt == nil && strings.EqualFold(a.DisplayName, agent.DisplayName) {
			return nil, domainerrors.ConflictError("agent name already exists", nil).WithReason("AGENT_NAME_TAKEN")
		}
	}
	s.actors = append(s.actors, agent)
	return agent, nil
}

func (s memActors) Revoke(_ context.Context, id string, at time.Time) (*model.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := s.get(id)
	if target == nil {
		return nil, domainerrors.NotFoundError("actor not found", nil)
	}
	for _, a := range s.actors {
		if (a.ID == id || a.OwnerActorID == id) && a.RevokedAt == nil {
			a.RevokedAt = &at
		}
	}
	return target, nil
}

func (s memTokens) Create(_ context.Context, token *model.Token, hash []byte) (*model.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, &memToken{token: token, hash: hash})
	return token, nil
}

func (s memTokens) Get(_ context.Context, id string) (*model.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tokens {
		if t.token.ID == id {
			return t.token, nil
		}
	}
	return nil, domainerrors.NotFoundError("token not found", nil)
}

// active: token sin revocar ni vencer, con su actor y el dueño sin revocar.
func (s memTokens) active(t *model.Token, now time.Time) bool {
	actor := memActors(s).get(t.ActorID)
	if actor == nil || actor.RevokedAt != nil || !t.IsActive(now) {
		return false
	}
	if actor.OwnerActorID != "" {
		if owner := memActors(s).get(actor.OwnerActorID); owner == nil || owner.RevokedAt != nil {
			return false
		}
	}
	return true
}

func (s memTokens) List(_ context.Context, actorIDs []string, includeInactive bool, now time.Time) ([]*model.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*model.Token{}
	for i := len(s.tokens) - 1; i >= 0; i-- {
		t := s.tokens[i].token
		if slices.Contains(actorIDs, t.ActorID) && (includeInactive || s.active(t, now)) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s memTokens) Revoke(_ context.Context, id string, at time.Time) (*model.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tokens {
		if t.token.ID == id {
			if t.token.RevokedAt == nil {
				t.token.RevokedAt = &at
			}
			return t.token, nil
		}
	}
	return nil, domainerrors.NotFoundError("token not found", nil)
}

func (s memTokens) Resolve(_ context.Context, hash []byte, now time.Time) (*model.ResolvedToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tokens {
		if !bytes.Equal(t.hash, hash) || !s.active(t.token, now) {
			continue
		}
		t.token.LastUsedAt = &now
		actor := memActors(s).get(t.token.ActorID)
		out := &model.ResolvedToken{TokenID: t.token.ID, Scopes: t.token.Scopes, ExpiresAt: t.token.ExpiresAt}
		if actor.Kind == model.ActorKindPerson {
			out.UserUID, out.PersonActorID = actor.UserUID, actor.ID
		} else {
			owner := memActors(s).get(actor.OwnerActorID)
			out.UserUID, out.PersonActorID, out.AgentID = owner.UserUID, owner.ID, actor.ID
		}
		return out, nil
	}
	return nil, domainerrors.NotFoundError("token not found", nil)
}

// Import en memoria: crea o deja igual (sin etiquetas ni cambios de estado,
// que cubre el repo de Postgres).
func (s memIssues) Import(_ context.Context, workspaceIDs []string, spec model.ImportSpec, author model.Author) (*model.Issue, model.ImportOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.projects[spec.ProjectID]; !ok || !slices.Contains(workspaceIDs, p.WorkspaceID) {
		return nil, "", domainerrors.NotFoundError("project not found", nil)
	}
	for _, it := range s.issues {
		if it.ProjectID != spec.ProjectID {
			continue
		}
		if it.SourceKey == spec.SourceKey {
			if it.Number != spec.Number {
				return nil, "", domainerrors.FailedPreconditionError("number mismatch", nil).WithReason("IMPORT_NUMBER_MISMATCH")
			}
			if it.Title == spec.Title {
				return it, model.ImportUnchanged, nil
			}
			s.addEvent(it.ID, author, &model.IssueEvent{Kind: model.EventTitleChanged, Title: &model.TitleChange{From: it.Title, To: spec.Title}})
			it.Title = spec.Title
			return it, model.ImportUpdated, nil
		}
		if it.Number == spec.Number {
			return nil, "", domainerrors.ConflictError("number taken", nil).WithReason("ISSUE_NUMBER_TAKEN")
		}
	}
	it := &model.Issue{ID: spec.SourceKey, ProjectID: spec.ProjectID, Number: spec.Number, Title: spec.Title,
		State: spec.State, Priority: spec.Priority, SourceKey: spec.SourceKey, LabelIDs: []string{}, CreatedAt: spec.CreatedAt}
	s.issues = append(s.issues, it)
	if s.next[spec.ProjectID] <= spec.Number {
		s.next[spec.ProjectID] = spec.Number + 1
	}
	s.addEvent(it.ID, author, &model.IssueEvent{Kind: model.EventCreated})
	return it, model.ImportCreated, nil
}

// ImportEvents en memoria: idempotente por SourceRef (sin overwrite ni el
// control de fechas, que cubre el repo de Postgres).
func (s memIssues) ImportEvents(_ context.Context, workspaceIDs []string, projectID, sourceKey string, events []model.ImportedEvent, _ bool, author model.Author) (model.ImportEventsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.projects[projectID]; !ok || !slices.Contains(workspaceIDs, p.WorkspaceID) {
		return model.ImportEventsResult{}, domainerrors.NotFoundError("project not found", nil)
	}
	var issue *model.Issue
	for _, it := range s.issues {
		if it.ProjectID == projectID && it.SourceKey == sourceKey {
			issue = it
		}
	}
	if issue == nil {
		return model.ImportEventsResult{}, domainerrors.NotFoundError("issue not found", nil)
	}
	var res model.ImportEventsResult
	for _, ev := range events {
		dup := false
		for _, e := range s.events {
			if e.IssueID == issue.ID && e.SourceRef == ev.SourceRef {
				dup = true
			}
		}
		if dup {
			res.Skipped++
			continue
		}
		e := &model.IssueEvent{Kind: model.EventStatusChanged, Status: ev.Status, Source: ev.Source, SourceRef: ev.SourceRef}
		s.addEvent(issue.ID, author, e)
		e.CreatedAt = ev.OccurredAt
		res.Created++
	}
	return res, nil
}

// asAgent es asUser con el x-agent-id que pone el gateway con un token de
// agente.
func asAgent(t *testing.T, uid, agentID string) context.Context {
	return metadata.AppendToOutgoingContext(asUser(t, uid), "x-agent-id", agentID)
}

func createAgent(t *testing.T, c clients, uid, name string) *issuesv1.Actor {
	t.Helper()
	res, err := c.actors.CreateAgent(asUser(t, uid), &issuesv1.CreateAgentRequest{DisplayName: name, Tool: issuesv1.AgentTool_AGENT_TOOL_CODEX})
	if err != nil {
		t.Fatalf("CreateAgent %s: %v", name, err)
	}
	return res.GetAgent()
}

func createToken(t *testing.T, c clients, uid, actorID string, scopes ...issuesv1.TokenScope) string {
	t.Helper()
	res, err := c.actors.CreateToken(asUser(t, uid), &issuesv1.CreateTokenRequest{ActorId: actorID, Scopes: scopes})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	return res.GetSecret()
}

func TestActorHandler_AgentsAndTokens(t *testing.T) {
	c := newClients(t)
	u1 := asUser(t, "u1")

	// CU-025-2: el agente queda con la persona como dueña.
	agent := createAgent(t, c, "u1", "Codex PC-01")
	listed, err := c.actors.ListActors(u1, &issuesv1.ListActorsRequest{})
	if err != nil || len(listed.GetActors()) != 2 {
		t.Fatalf("ListActors: %v %v", listed, err)
	}
	person := listed.GetActors()[0]
	if person.GetKind() != issuesv1.ActorKind_ACTOR_KIND_PERSON || person.GetUserId() != "u1" {
		t.Errorf("person first: got %+v", person)
	}
	if agent.GetOwnerActorId() != person.GetId() || agent.GetTool() != issuesv1.AgentTool_AGENT_TOOL_CODEX || agent.GetKind() != issuesv1.ActorKind_ACTOR_KIND_AGENT {
		t.Errorf("agent: got %+v", agent)
	}
	// ... y otra persona no puede crear un agente a su nombre (403).
	_, err = c.actors.CreateAgent(asUser(t, "u2"), &issuesv1.CreateAgentRequest{DisplayName: "x", Tool: issuesv1.AgentTool_AGENT_TOOL_CODEX, OwnerActorId: person.GetId()})
	wantCode(t, "agent for someone else", err, codes.PermissionDenied)
	_, err = c.actors.CreateAgent(u1, &issuesv1.CreateAgentRequest{DisplayName: "codex pc-01", Tool: issuesv1.AgentTool_AGENT_TOOL_CODEX})
	wantCode(t, "duplicate name", err, codes.AlreadyExists)
	_, err = c.actors.CreateAgent(u1, &issuesv1.CreateAgentRequest{DisplayName: "x"})
	wantCode(t, "tool unspecified", err, codes.InvalidArgument)
	// Un token (agente) no crea agentes ni tokens.
	_, err = c.actors.CreateAgent(asAgent(t, "u1", agent.GetId()), &issuesv1.CreateAgentRequest{DisplayName: "y", Tool: issuesv1.AgentTool_AGENT_TOOL_OTHER})
	wantCode(t, "agent creating agents", err, codes.PermissionDenied)
	_, err = c.actors.CreateToken(asAgent(t, "u1", agent.GetId()), &issuesv1.CreateTokenRequest{Scopes: []issuesv1.TokenScope{issuesv1.TokenScope_TOKEN_SCOPE_ISSUES_READ}})
	wantCode(t, "agent creating tokens", err, codes.PermissionDenied)
	_, err = c.actors.ListActors(asUser(t, ""), &issuesv1.ListActorsRequest{})
	wantCode(t, "no identity", err, codes.Unauthenticated)

	// Token del agente: se muestra una vez y se resuelve a dueño + agente.
	res, err := c.actors.CreateToken(u1, &issuesv1.CreateTokenRequest{
		ActorId: agent.GetId(), Name: "ci",
		Scopes: []issuesv1.TokenScope{issuesv1.TokenScope_TOKEN_SCOPE_ISSUES_WRITE, issuesv1.TokenScope_TOKEN_SCOPE_ISSUES_READ, issuesv1.TokenScope_TOKEN_SCOPE_ISSUES_READ},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	secret := res.GetSecret()
	if !strings.HasPrefix(secret, "tka_") || len(secret) != 4+43 {
		t.Errorf("secret format: %q", secret)
	}
	tok := res.GetToken()
	wantScopes := []issuesv1.TokenScope{issuesv1.TokenScope_TOKEN_SCOPE_ISSUES_READ, issuesv1.TokenScope_TOKEN_SCOPE_ISSUES_WRITE}
	if !slices.Equal(tok.GetScopes(), wantScopes) || tok.GetActorId() != agent.GetId() || tok.GetExpiresAt() == nil {
		t.Errorf("token: got %+v", tok)
	}
	if d := tok.GetExpiresAt().AsTime().Sub(tok.GetCreatedAt().AsTime()); d != 30*24*time.Hour {
		t.Errorf("default expiry: got %v", d)
	}
	resolved, err := c.auth.ResolveToken(asUser(t, ""), &issuesv1.ResolveTokenRequest{Token: secret})
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if resolved.GetUserId() != "u1" || resolved.GetAgentId() != agent.GetId() || resolved.GetPersonActorId() != person.GetId() ||
		resolved.GetTokenId() != tok.GetId() || !slices.Equal(resolved.GetScopes(), wantScopes) {
		t.Errorf("ResolveToken: got %+v", resolved)
	}
	// CU-025-7: ninguna respuesta lleva el token ni su hash (solo CreateToken
	// lo devuelve, una vez).
	tokens, err := c.actors.ListTokens(u1, &issuesv1.ListTokensRequest{})
	if err != nil || len(tokens.GetTokens()) != 1 || strings.Contains(tokens.String(), secret[4:]) {
		t.Errorf("ListTokens: %v %v", tokens, err)
	}
	if tokens.GetTokens()[0].GetLastUsedAt() == nil {
		t.Errorf("last_used_at not set after ResolveToken")
	}

	for _, bad := range []string{"", "tka_corto", "gwk_" + secret[4:], secret + "x", "tka_" + strings.Repeat("A", 43)} {
		_, err := c.auth.ResolveToken(asUser(t, ""), &issuesv1.ResolveTokenRequest{Token: bad})
		wantCode(t, "resolve "+bad, err, codes.Unauthenticated)
	}

	// Vencimiento: más de 90 días o en el pasado es INVALID_ARGUMENT.
	for name, at := range map[string]time.Time{"91 days": time.Now().Add(91 * 24 * time.Hour), "past": time.Now().Add(-time.Minute)} {
		_, err := c.actors.CreateToken(u1, &issuesv1.CreateTokenRequest{Scopes: wantScopes, ExpiresAt: timestamppb.New(at)})
		wantCode(t, "expiry "+name, err, codes.InvalidArgument)
	}
	_, err = c.actors.CreateToken(u1, &issuesv1.CreateTokenRequest{})
	wantCode(t, "no scopes", err, codes.InvalidArgument)
	_, err = c.actors.CreateToken(u1, &issuesv1.CreateTokenRequest{Scopes: []issuesv1.TokenScope{issuesv1.TokenScope_TOKEN_SCOPE_UNSPECIFIED}})
	wantCode(t, "unspecified scope", err, codes.InvalidArgument)

	// Ajenos: NOT_FOUND.
	_, err = c.actors.RevokeToken(asUser(t, "u2"), &issuesv1.RevokeTokenRequest{TokenId: tok.GetId()})
	wantCode(t, "revoke someone else's token", err, codes.NotFound)
	_, err = c.actors.RevokeActor(asUser(t, "u2"), &issuesv1.RevokeActorRequest{ActorId: agent.GetId()})
	wantCode(t, "revoke someone else's agent", err, codes.NotFound)
	_, err = c.actors.CreateToken(asUser(t, "u2"), &issuesv1.CreateTokenRequest{ActorId: agent.GetId(), Scopes: wantScopes})
	wantCode(t, "token for someone else's agent", err, codes.NotFound)
	_, err = c.actors.ListTokens(asUser(t, "u2"), &issuesv1.ListTokensRequest{ActorId: agent.GetId()})
	wantCode(t, "list someone else's tokens", err, codes.NotFound)

	// CU-025-7: revocado responde UNAUTHENTICATED. Revocar dos veces no
	// cambia nada.
	if _, err := c.actors.RevokeToken(u1, &issuesv1.RevokeTokenRequest{TokenId: tok.GetId()}); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	again, err := c.actors.RevokeToken(u1, &issuesv1.RevokeTokenRequest{TokenId: tok.GetId()})
	if err != nil || again.GetToken().GetRevokedAt() == nil {
		t.Errorf("RevokeToken again: %v %v", again, err)
	}
	_, err = c.auth.ResolveToken(asUser(t, ""), &issuesv1.ResolveTokenRequest{Token: secret})
	wantCode(t, "revoked token", err, codes.Unauthenticated)
	if active, _ := c.actors.ListTokens(u1, &issuesv1.ListTokensRequest{}); len(active.GetTokens()) != 0 {
		t.Errorf("revoked token listed as active: %v", active)
	}
	if all, _ := c.actors.ListTokens(u1, &issuesv1.ListTokensRequest{IncludeInactive: true}); len(all.GetTokens()) != 1 {
		t.Errorf("include_inactive: %v", all)
	}
}

// CU-025-6: revocar a una persona deja sin efecto los tokens de sus dos
// agentes (y el suyo), y no se le pueden emitir más.
func TestActorHandler_RevokePersonCascades(t *testing.T) {
	c := newClients(t)
	read := issuesv1.TokenScope_TOKEN_SCOPE_ISSUES_READ
	a1, a2 := createAgent(t, c, "u1", "A1"), createAgent(t, c, "u1", "A2")
	secrets := []string{createToken(t, c, "u1", a1.GetId(), read), createToken(t, c, "u1", a2.GetId(), read), createToken(t, c, "u1", "", read)}
	other := createToken(t, c, "u2", "", read)
	for i, s := range secrets {
		if _, err := c.auth.ResolveToken(asUser(t, ""), &issuesv1.ResolveTokenRequest{Token: s}); err != nil {
			t.Fatalf("resolve %d before revoke: %v", i, err)
		}
	}

	person := a1.GetOwnerActorId()
	res, err := c.actors.RevokeActor(asUser(t, "u1"), &issuesv1.RevokeActorRequest{ActorId: person})
	if err != nil || res.GetActor().GetRevokedAt() == nil {
		t.Fatalf("RevokeActor: %v %v", res, err)
	}
	for _, s := range secrets {
		_, err := c.auth.ResolveToken(asUser(t, ""), &issuesv1.ResolveTokenRequest{Token: s})
		wantCode(t, "token after revoking the person", err, codes.Unauthenticated)
	}
	if _, err := c.auth.ResolveToken(asUser(t, ""), &issuesv1.ResolveTokenRequest{Token: other}); err != nil {
		t.Errorf("another person's token: %v", err)
	}
	all, err := c.actors.ListActors(asUser(t, "u1"), &issuesv1.ListActorsRequest{IncludeRevoked: true})
	if err != nil || len(all.GetActors()) != 3 {
		t.Fatalf("ListActors include_revoked: %v %v", all, err)
	}
	for _, a := range all.GetActors() {
		if a.GetRevokedAt() == nil {
			t.Errorf("actor %s not revoked", a.GetDisplayName())
		}
	}
	if active, _ := c.actors.ListActors(asUser(t, "u1"), &issuesv1.ListActorsRequest{}); len(active.GetActors()) != 0 {
		t.Errorf("revoked actors listed: %v", active)
	}
	_, err = c.actors.CreateToken(asUser(t, "u1"), &issuesv1.CreateTokenRequest{Scopes: []issuesv1.TokenScope{read}})
	wantCode(t, "token for a revoked person", err, codes.FailedPrecondition)
	_, err = c.actors.CreateAgent(asUser(t, "u1"), &issuesv1.CreateAgentRequest{DisplayName: "A3", Tool: issuesv1.AgentTool_AGENT_TOOL_CLOUD})
	wantCode(t, "agent for a revoked person", err, codes.FailedPrecondition)
}

// CU-025-4 y CU-025-8: autoría con agente en el historial y filtro por
// asignado.
func TestIssueHandler_AgentAuthorAndAssignee(t *testing.T) {
	c := newClients(t)
	u1 := asUser(t, "u1")
	project, err := c.issues.CreateProject(u1, &issuesv1.CreateProjectRequest{Key: "TK", Name: "Tekitla"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := project.GetProject().GetId()
	a1, a2 := createAgent(t, c, "u1", "A1"), createAgent(t, c, "u1", "A2")
	person := a1.GetOwnerActorId()

	for i, who := range []string{person, a1.GetId(), a2.GetId(), ""} {
		res, err := c.issues.CreateIssue(u1, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: "issue", AssigneeActorId: who})
		if err != nil || res.GetIssue().GetAssigneeActorId() != who {
			t.Fatalf("CreateIssue %d: %v %v", i, res, err)
		}
	}
	list := func(req *issuesv1.ListIssuesRequest) []int32 {
		t.Helper()
		req.ProjectId = pid
		res, err := c.issues.ListIssues(u1, req)
		if err != nil {
			t.Fatalf("ListIssues: %v", err)
		}
		var numbers []int32
		for _, it := range res.GetIssues() {
			numbers = append(numbers, it.GetNumber())
		}
		return numbers
	}
	if got := list(&issuesv1.ListIssuesRequest{AssigneeActorIds: []string{a1.GetId()}}); !slices.Equal(got, []int32{2}) {
		t.Errorf("issues of A1: got %v", got)
	}
	if got := list(&issuesv1.ListIssuesRequest{AssigneeActorIds: []string{a2.GetId()}, Unassigned: true}); !slices.Equal(got, []int32{3, 4}) {
		t.Errorf("A2 or unassigned: got %v", got)
	}
	if got := list(&issuesv1.ListIssuesRequest{Unassigned: true}); !slices.Equal(got, []int32{4}) {
		t.Errorf("unassigned: got %v", got)
	}
	if got := list(&issuesv1.ListIssuesRequest{AssigneeActorIds: []string{"no-es-un-uuid"}}); len(got) != 0 {
		t.Errorf("unknown actor: got %v", got)
	}

	// Asignar a un actor ajeno, revocado o inexistente es INVALID_ARGUMENT.
	foreign := createAgent(t, c, "u2", "B1")
	for name, id := range map[string]string{"foreign": foreign.GetId(), "unknown": "00000000-0000-4000-8000-000000000099", "not uuid": "x"} {
		_, err := c.issues.UpdateIssue(u1, &issuesv1.UpdateIssueRequest{ProjectId: pid, Number: 4, AssigneeActorId: &id})
		wantCode(t, "assign "+name, err, codes.InvalidArgument)
	}

	// Un agente de u1 asigna el issue 4 a A2 y lo mueve: los eventos quedan a
	// nombre de u1 con agent_id = A1.
	agentCtx := asAgent(t, "u1", a1.GetId())
	to := a2.GetId()
	if _, err := c.issues.UpdateIssue(agentCtx, &issuesv1.UpdateIssueRequest{ProjectId: pid, Number: 4, AssigneeActorId: &to}); err != nil {
		t.Fatalf("UpdateIssue assignee: %v", err)
	}
	if _, err := c.issues.MoveIssue(agentCtx, &issuesv1.MoveIssueRequest{ProjectId: pid, Number: 4, State: issuesv1.IssueState_ISSUE_STATE_DONE}); err != nil {
		t.Fatalf("MoveIssue: %v", err)
	}
	events, err := c.issues.ListIssueEvents(u1, &issuesv1.ListIssueEventsRequest{ProjectId: pid, Number: 4})
	if err != nil {
		t.Fatalf("ListIssueEvents: %v", err)
	}
	evs := events.GetEvents()
	if len(evs) != 3 {
		t.Fatalf("events: got %d", len(evs))
	}
	if evs[0].GetAgentId() != "" || evs[0].GetActorId() != "u1" {
		t.Errorf("created by the person: got %+v", evs[0])
	}
	assigned := evs[1].GetAssigneeChanged()
	if assigned == nil || assigned.GetToActorId() != a2.GetId() || assigned.GetFromActorId() != "" {
		t.Errorf("assignee_changed: got %+v", evs[1])
	}
	for _, ev := range evs[1:] {
		if ev.GetActorId() != "u1" || ev.GetAgentId() != a1.GetId() {
			t.Errorf("event by the agent: actor %q agent %q", ev.GetActorId(), ev.GetAgentId())
		}
	}
	if got := list(&issuesv1.ListIssuesRequest{AssigneeActorIds: []string{a2.GetId()}}); !slices.Equal(got, []int32{3, 4}) {
		t.Errorf("issues of A2 after assigning: got %v", got)
	}

	// Un x-agent-id que no es un UUID no lo pudo poner el gateway.
	_, err = c.issues.MoveIssue(asAgent(t, "u1", "x"), &issuesv1.MoveIssueRequest{ProjectId: pid, Number: 4, State: issuesv1.IssueState_ISSUE_STATE_TODO})
	wantCode(t, "bad agent id", err, codes.Unauthenticated)
}

func TestIssueHandler_ImportIssue(t *testing.T) {
	c := newClients(t)
	u1 := asUser(t, "u1")
	project, err := c.issues.CreateProject(u1, &issuesv1.CreateProjectRequest{Key: "RND", Name: "Round"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := project.GetProject().GetId()
	req := &issuesv1.ImportIssueRequest{
		ProjectId: pid, SourceKey: "236-claude-md-en-capas", Number: 236, Title: "CLAUDE.md en capas", //gitleaks:allow
		State: issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS, Priority: issuesv1.Priority_PRIORITY_HIGH,
		CreatedAt: timestamppb.New(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)),
	}
	res, err := c.issues.ImportIssue(u1, req)
	if err != nil || res.GetOutcome() != issuesv1.ImportOutcome_IMPORT_OUTCOME_CREATED {
		t.Fatalf("ImportIssue: %v %v", res, err)
	}
	if it := res.GetIssue(); it.GetNumber() != 236 || it.GetSourceKey() != "236-claude-md-en-capas" ||
		it.GetState() != issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS || it.GetPriority() != issuesv1.Priority_PRIORITY_HIGH {
		t.Errorf("imported: got %+v", it)
	}
	res, err = c.issues.ImportIssue(u1, req)
	if err != nil || res.GetOutcome() != issuesv1.ImportOutcome_IMPORT_OUTCOME_UNCHANGED {
		t.Errorf("ImportIssue again: %v %v", res, err)
	}
	// El siguiente CreateIssue usa un número mayor que los importados.
	created, err := c.issues.CreateIssue(u1, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: "nuevo"})
	if err != nil || created.GetIssue().GetNumber() != 237 {
		t.Errorf("CreateIssue after import: %v %v", created, err)
	}

	with := func(change func(r *issuesv1.ImportIssueRequest)) *issuesv1.ImportIssueRequest {
		r := proto.Clone(req).(*issuesv1.ImportIssueRequest)
		change(r)
		return r
	}
	_, err = c.issues.ImportIssue(u1, with(func(r *issuesv1.ImportIssueRequest) { r.Number = 300 }))
	wantCode(t, "number mismatch", err, codes.FailedPrecondition)
	_, err = c.issues.ImportIssue(u1, with(func(r *issuesv1.ImportIssueRequest) { r.SourceKey = "236-otro" }))
	wantCode(t, "number taken", err, codes.AlreadyExists)
	_, err = c.issues.ImportIssue(u1, with(func(r *issuesv1.ImportIssueRequest) { r.State = issuesv1.IssueState_ISSUE_STATE_UNSPECIFIED }))
	wantCode(t, "state required", err, codes.InvalidArgument)
	_, err = c.issues.ImportIssue(u1, with(func(r *issuesv1.ImportIssueRequest) { r.SourceKey = "  " }))
	wantCode(t, "source_key required", err, codes.InvalidArgument)
	_, err = c.issues.ImportIssue(asUser(t, "u2"), req)
	wantCode(t, "someone else's project", err, codes.NotFound)
	_, err = c.issues.ImportIssue(asAgent(t, "u1", "00000000-0000-4000-8000-000000000001"), req)
	wantCode(t, "import by an agent", err, codes.PermissionDenied)
}

// ImportIssueEvents (issue 036): eventos con fecha y fuente, idempotente
// por source_ref, solo para una persona; ListIssueEvents trae la fuente.
func TestIssueHandler_ImportIssueEvents(t *testing.T) {
	c := newClients(t)
	u1 := asUser(t, "u1")
	project, err := c.issues.CreateProject(u1, &issuesv1.CreateProjectRequest{Key: "RND", Name: "Round"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := project.GetProject().GetId()
	if _, err := c.issues.ImportIssue(u1, &issuesv1.ImportIssueRequest{
		ProjectId: pid, SourceKey: "007-cola", Number: 7, Title: "Cola", State: issuesv1.IssueState_ISSUE_STATE_DONE,
		CreatedAt: timestamppb.New(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)),
	}); err != nil {
		t.Fatalf("ImportIssue: %v", err)
	}
	ev := func(ref string, src issuesv1.EventSource, at time.Time, from, to issuesv1.IssueState) *issuesv1.ImportedEvent {
		return &issuesv1.ImportedEvent{SourceRef: ref, Source: src, OccurredAt: timestamppb.New(at),
			Change: &issuesv1.ImportedEvent_StatusChanged{StatusChanged: &issuesv1.ImportedStatusChange{
				FromState: from, ToState: to, FromName: "open", ToName: "resolved",
			}}}
	}
	req := &issuesv1.ImportIssueEventsRequest{ProjectId: pid, SourceKey: "007-cola", Events: []*issuesv1.ImportedEvent{
		ev("abc123", issuesv1.EventSource_EVENT_SOURCE_IMPORT_HISTORY, time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC),
			issuesv1.IssueState_ISSUE_STATE_TODO, issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS),
		ev("inicial", issuesv1.EventSource_EVENT_SOURCE_IMPORT_FILE, time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
			issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS, issuesv1.IssueState_ISSUE_STATE_DONE),
	}}
	res, err := c.issues.ImportIssueEvents(u1, req)
	if err != nil || res.GetCreated() != 2 || res.GetSkipped() != 0 {
		t.Fatalf("ImportIssueEvents: %+v %v", res, err)
	}
	// Segunda corrida: no agrega nada (CU-036-1).
	res, err = c.issues.ImportIssueEvents(u1, req)
	if err != nil || res.GetCreated() != 0 || res.GetSkipped() != 2 {
		t.Errorf("ImportIssueEvents again: %+v %v", res, err)
	}
	events, err := c.issues.ListIssueEvents(u1, &issuesv1.ListIssueEventsRequest{ProjectId: pid, Number: 7})
	if err != nil {
		t.Fatalf("ListIssueEvents: %v", err)
	}
	var sources []issuesv1.EventSource
	var refs []string
	for _, e := range events.GetEvents() {
		sources = append(sources, e.GetSource())
		refs = append(refs, e.GetSourceRef())
	}
	if !slices.Equal(refs, []string{"", "abc123", "inicial"}) || sources[1] != issuesv1.EventSource_EVENT_SOURCE_IMPORT_HISTORY ||
		sources[2] != issuesv1.EventSource_EVENT_SOURCE_IMPORT_FILE {
		t.Errorf("events: refs %v sources %v", refs, sources)
	}
	if sc := events.GetEvents()[2].GetStatusChanged(); sc.GetToState() != issuesv1.IssueState_ISSUE_STATE_DONE || sc.GetToStatusId() != "" || sc.GetToStatusName() != "resolved" {
		t.Errorf("imported status change: %+v", sc)
	}

	// Un agente no importa; un issue sin esa clave es NOT_FOUND; un evento
	// sin cambio o con fuente LIVE es INVALID_ARGUMENT.
	a1 := createAgent(t, c, "u1", "A1")
	_, err = c.issues.ImportIssueEvents(asAgent(t, "u1", a1.GetId()), req)
	wantCode(t, "agent", err, codes.PermissionDenied)
	_, err = c.issues.ImportIssueEvents(u1, &issuesv1.ImportIssueEventsRequest{ProjectId: pid, SourceKey: "999-otro", Events: req.GetEvents()})
	wantCode(t, "unknown source key", err, codes.NotFound)
	bad := proto.Clone(req).(*issuesv1.ImportIssueEventsRequest)
	bad.Events[0].Change = nil
	_, err = c.issues.ImportIssueEvents(u1, bad)
	wantCode(t, "no change", err, codes.InvalidArgument)
	bad = proto.Clone(req).(*issuesv1.ImportIssueEventsRequest)
	bad.Events[0].Source = issuesv1.EventSource_EVENT_SOURCE_LIVE
	_, err = c.issues.ImportIssueEvents(u1, bad)
	wantCode(t, "live source", err, codes.InvalidArgument)
	_, err = c.issues.ImportIssueEvents(asUser(t, "u2"), req)
	wantCode(t, "foreign project", err, codes.NotFound)
}

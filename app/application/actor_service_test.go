package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

var clockNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

const (
	personID   = "00000000-0000-4000-8000-0000000000a1"
	agentID    = "00000000-0000-4000-8000-0000000000b1"
	otherAgent = "00000000-0000-4000-8000-0000000000b2"
)

// actorsOf: u1 es personID y tiene un agente agentID; otherAgent es de otra
// persona.
func actorsOf(revokedPerson bool) *mockActorRepo {
	person := &model.Actor{ID: personID, Kind: model.ActorKindPerson, UserUID: owner}
	if revokedPerson {
		at := clockNow.Add(-time.Hour)
		person.RevokedAt = &at
	}
	return &mockActorRepo{
		ensurePersonFn: func(_ context.Context, p *model.Actor) (*model.Actor, error) {
			if p.UserUID != owner {
				return &model.Actor{ID: "00000000-0000-4000-8000-0000000000a2", Kind: model.ActorKindPerson, UserUID: p.UserUID}, nil
			}
			return person, nil
		},
		getFn: func(_ context.Context, id string) (*model.Actor, error) {
			switch id {
			case agentID:
				return &model.Actor{ID: agentID, Kind: model.ActorKindAgent, OwnerActorID: personID, Tool: model.AgentToolCodex}, nil
			case otherAgent:
				return &model.Actor{ID: otherAgent, Kind: model.ActorKindAgent, OwnerActorID: "00000000-0000-4000-8000-0000000000a2"}, nil
			}
			return nil, domainerrors.NotFoundError("actor not found", nil)
		},
		listAgentsFn: func(context.Context, string, bool) ([]*model.Actor, error) {
			return []*model.Actor{{ID: agentID, Kind: model.ActorKindAgent, OwnerActorID: personID}}, nil
		},
	}
}

func actorSvc(actors *mockActorRepo, tokens *mockTokenRepo) *application.ActorService {
	return application.NewActorService(actors, tokens).WithClock(func() time.Time { return clockNow })
}

func TestActorService_CreateTokenStoresOnlyTheHash(t *testing.T) {
	var gotHash []byte
	var gotToken *model.Token
	tokens := &mockTokenRepo{createFn: func(_ context.Context, tok *model.Token, hash []byte) (*model.Token, error) {
		gotToken, gotHash = tok, hash
		return tok, nil
	}}
	tok, secret, err := actorSvc(actorsOf(false), tokens).CreateToken(context.Background(), owner, application.CreateTokenInput{
		ActorID: agentID, Name: "  ci  ", Scopes: []model.TokenScope{model.TokenScopeIssuesWrite, model.TokenScopeIssuesRead},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	sum := sha256.Sum256([]byte(secret))
	if !bytes.Equal(gotHash, sum[:]) || bytes.Contains(gotHash, []byte(secret)) {
		t.Errorf("stored hash is not sha256(secret)")
	}
	if !strings.HasPrefix(secret, model.TokenPrefix) || !model.LooksLikeToken(secret) {
		t.Errorf("secret format: %q", secret)
	}
	if gotToken.ActorID != agentID || gotToken.Name != "ci" || !gotToken.ExpiresAt.Equal(clockNow.Add(30*24*time.Hour)) {
		t.Errorf("token: %+v", gotToken)
	}
	if !slices.Equal(tok.Scopes, []model.TokenScope{model.TokenScopeIssuesRead, model.TokenScopeIssuesWrite}) {
		t.Errorf("scopes in enum order: %v", tok.Scopes)
	}

	// Dos tokens nunca son iguales.
	_, second, _ := actorSvc(actorsOf(false), tokens).CreateToken(context.Background(), owner, application.CreateTokenInput{Scopes: []model.TokenScope{model.TokenScopeIssuesRead}})
	if second == secret {
		t.Errorf("secrets repeat")
	}
}

func TestActorService_CreateTokenValidation(t *testing.T) {
	svc := actorSvc(actorsOf(false), &mockTokenRepo{})
	ctx := context.Background()
	read := []model.TokenScope{model.TokenScopeIssuesRead}
	at := func(d time.Duration) *time.Time { v := clockNow.Add(d); return &v }

	if _, _, err := svc.CreateToken(ctx, owner, application.CreateTokenInput{Scopes: read, ExpiresAt: at(90 * 24 * time.Hour)}); err != nil {
		t.Errorf("90 days exactly: %v", err)
	}
	for name, in := range map[string]application.CreateTokenInput{
		"no scopes":      {},
		"bad scope":      {Scopes: []model.TokenScope{"issues:admin"}},
		"past":           {Scopes: read, ExpiresAt: at(-time.Second)},
		"now":            {Scopes: read, ExpiresAt: at(0)},
		"over 90 days":   {Scopes: read, ExpiresAt: at(90*24*time.Hour + time.Second)},
		"long name":      {Scopes: read, Name: strings.Repeat("x", 81)},
		"actor not uuid": {Scopes: read, ActorID: "x"},
	} {
		if _, _, err := svc.CreateToken(ctx, owner, in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: want validation, got %v", name, err)
		}
	}
	if _, _, err := svc.CreateToken(ctx, owner, application.CreateTokenInput{Scopes: read, ActorID: otherAgent}); !domainerrors.IsNotFound(err) {
		t.Errorf("someone else's agent: want not found, got %v", err)
	}
	if _, _, err := actorSvc(actorsOf(true), &mockTokenRepo{}).CreateToken(ctx, owner, application.CreateTokenInput{Scopes: read, ActorID: agentID}); !domainerrors.IsFailedPrecondition(err) {
		t.Errorf("agent of a revoked person: want failed precondition, got %v", err)
	}
}

func TestActorService_ResolveToken(t *testing.T) {
	ctx := context.Background()
	called := 0
	tokens := &mockTokenRepo{resolveFn: func(_ context.Context, _ []byte, now time.Time) (*model.ResolvedToken, error) {
		called++
		if !now.Equal(clockNow) {
			t.Errorf("resolve now: %v", now)
		}
		return nil, domainerrors.NotFoundError("token not found", nil)
	}}
	svc := actorSvc(actorsOf(false), tokens)
	// Mal formado: ni se busca.
	if _, err := svc.ResolveToken(ctx, "Bearer abc"); !domainerrors.IsUnauthorized(err) || called != 0 {
		t.Errorf("malformed: %v (lookups %d)", err, called)
	}
	// No existe / vencido / revocado: el repo dice NotFound y sale
	// Unauthorized sin detalle.
	_, err := svc.ResolveToken(ctx, model.TokenPrefix+strings.Repeat("A", 43))
	var de *domainerrors.DomainError
	if !errors.As(err, &de) || !domainerrors.IsUnauthorized(err) || len(de.Details) != 0 || called != 1 {
		t.Errorf("unknown: %v", err)
	}
	boom := domainerrors.InternalError("db down", nil)
	tokens.resolveFn = func(context.Context, []byte, time.Time) (*model.ResolvedToken, error) { return nil, boom }
	if _, err := svc.ResolveToken(ctx, model.TokenPrefix+strings.Repeat("A", 43)); !errors.Is(err, boom) {
		t.Errorf("repo error must propagate (503, not 401): %v", err)
	}
}

func TestActorService_AgentsAndRevocation(t *testing.T) {
	ctx := context.Background()
	if _, err := actorSvc(actorsOf(false), nil).CreateAgent(ctx, owner, application.CreateAgentInput{DisplayName: "x", Tool: model.AgentToolCodex, OwnerActorID: "00000000-0000-4000-8000-0000000000a2"}); !domainerrors.IsPermissionDenied(err) {
		t.Errorf("agent for someone else: %v", err)
	}
	if _, err := actorSvc(actorsOf(false), nil).CreateAgent(ctx, owner, application.CreateAgentInput{DisplayName: " ", Tool: model.AgentToolCodex}); !domainerrors.IsValidation(err) {
		t.Errorf("empty name: %v", err)
	}
	agent, err := actorSvc(actorsOf(false), nil).CreateAgent(ctx, owner, application.CreateAgentInput{DisplayName: " Codex PC-01 ", Tool: model.AgentToolCodex})
	if err != nil || agent.OwnerActorID != personID || agent.DisplayName != "Codex PC-01" || !agent.CreatedAt.Equal(clockNow) {
		t.Errorf("CreateAgent: %+v %v", agent, err)
	}

	var revoked string
	actors := actorsOf(false)
	actors.revokeFn = func(_ context.Context, id string, at time.Time) (*model.Actor, error) {
		revoked = id
		return &model.Actor{ID: id, RevokedAt: &at}, nil
	}
	if _, err := actorSvc(actors, nil).RevokeActor(ctx, owner, agentID); err != nil || revoked != agentID {
		t.Errorf("revoke own agent: %v (%s)", err, revoked)
	}
	if _, err := actorSvc(actors, nil).RevokeActor(ctx, owner, otherAgent); !domainerrors.IsNotFound(err) {
		t.Errorf("revoke someone else's agent: %v", err)
	}
	revoked = ""
	if a, err := actorSvc(actorsOf(true), nil).RevokeActor(ctx, owner, personID); err != nil || a.RevokedAt == nil || revoked != "" {
		t.Errorf("revoke an already revoked person must not write: %v", err)
	}
}

func TestIssueService_AssigneeValidation(t *testing.T) {
	ctx := context.Background()
	var gotPatch model.IssuePatch
	issues := &mockIssueRepo{updateFn: func(_ context.Context, _ string, _ int32, patch model.IssuePatch, _ model.Author) (*model.Issue, error) {
		gotPatch = patch
		return &model.Issue{}, nil
	}}
	svc := application.NewIssueService(personalWorkspaces(), ownedProjects(), issues, projectStatuses(), &mockLabelRepo{}, actorsOf(false))
	for _, id := range []string{agentID, personID, ""} {
		id := id
		if _, err := svc.UpdateIssue(ctx, as(owner), application.UpdateIssueInput{ProjectID: validID, Number: 1, AssigneeActorID: &id}); err != nil || *gotPatch.AssigneeActorID != id {
			t.Errorf("assign %q: %v", id, err)
		}
	}
	for _, id := range []string{otherAgent, "00000000-0000-4000-8000-0000000000ff", "x"} {
		id := id
		if _, err := svc.UpdateIssue(ctx, as(owner), application.UpdateIssueInput{ProjectID: validID, Number: 1, AssigneeActorID: &id}); !domainerrors.IsValidation(err) {
			t.Errorf("assign %q: want validation, got %v", id, err)
		}
	}
	revokedSvc := application.NewIssueService(personalWorkspaces(), ownedProjects(), issues, projectStatuses(), &mockLabelRepo{}, actorsOf(true))
	if _, err := revokedSvc.CreateIssue(ctx, as(owner), application.CreateIssueInput{ProjectID: validID, Title: "x", AssigneeActorID: agentID}); !domainerrors.IsValidation(err) {
		t.Errorf("assign an agent of a revoked person: %v", err)
	}
}

func TestIssueService_ImportIssue(t *testing.T) {
	ctx := context.Background()
	var got model.ImportSpec
	issues := &mockIssueRepo{importFn: func(_ context.Context, _ []string, spec model.ImportSpec, _ model.Author) (*model.Issue, model.ImportOutcome, error) {
		got = spec
		return &model.Issue{}, model.ImportCreated, nil
	}}
	svc := application.NewIssueService(personalWorkspaces(), ownedProjects(), issues, projectStatuses(), &mockLabelRepo{}, &mockActorRepo{})
	_, outcome, err := svc.ImportIssue(ctx, as(owner), application.ImportIssueInput{
		ProjectID: validID, SourceKey: " 236-claude-md-en-capas ", Number: 236, Title: " CLAUDE.md ",
		State: model.IssueStateInProgress, LabelNames: []string{"infra", " Infra ", "backend"},
	})
	if err != nil || outcome != model.ImportCreated {
		t.Fatalf("ImportIssue: %v", err)
	}
	if got.SourceKey != "236-claude-md-en-capas" || got.Title != "CLAUDE.md" || got.Priority != model.PriorityMedium ||
		got.CreatedAt.IsZero() || !slices.Equal(got.LabelNames, []string{"infra", "backend"}) {
		t.Errorf("spec: %+v", got)
	}
	for name, in := range map[string]application.ImportIssueInput{
		"no key":    {ProjectID: validID, Number: 1, Title: "x", State: model.IssueStateTodo},
		"long key":  {ProjectID: validID, SourceKey: strings.Repeat("k", 201), Number: 1, Title: "x", State: model.IssueStateTodo},
		"number 0":  {ProjectID: validID, SourceKey: "k", Title: "x", State: model.IssueStateTodo},
		"no title":  {ProjectID: validID, SourceKey: "k", Number: 1, State: model.IssueStateTodo},
		"no state":  {ProjectID: validID, SourceKey: "k", Number: 1, Title: "x"},
		"bad label": {ProjectID: validID, SourceKey: "k", Number: 1, Title: "x", State: model.IssueStateTodo, LabelNames: []string{" "}},
	} {
		if _, _, err := svc.ImportIssue(ctx, as(owner), in); !domainerrors.IsValidation(err) {
			t.Errorf("%s: want validation, got %v", name, err)
		}
	}
}

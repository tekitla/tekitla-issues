package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

const shareLinkID = "00000000-0000-4000-8000-0000000000c1"

// validShareToken tiene la forma de un token de enlace (no hace falta que
// exista: el mock decide).
var validShareToken = "tks_" + strings.Repeat("A", 43)

func shareSvc(links *mockShareLinkRepo, issues *mockIssueRepo, actors *mockActorRepo) *application.ShareLinkService {
	return shareSvcWith(links, issues, actors, &mockStatusRepo{}, &mockLabelRepo{})
}

func shareSvcWith(links *mockShareLinkRepo, issues *mockIssueRepo, actors *mockActorRepo, statuses *mockStatusRepo, labels *mockLabelRepo) *application.ShareLinkService {
	if issues == nil {
		issues = &mockIssueRepo{}
	}
	if actors == nil {
		actors = &mockActorRepo{}
	}
	return application.NewShareLinkService(personalWorkspaces(), ownedProjects(), links, issues,
		statuses, labels, actors).WithClock(func() time.Time { return clockNow })
}

// resolvesTo es un repo cuyo Resolve devuelve un enlace activo del proyecto
// validID con ese alcance, creado por creator (owner si no se dice).
func resolvesTo(scope model.ShareScope, creator ...string) *mockShareLinkRepo {
	by := owner
	if len(creator) > 0 {
		by = creator[0]
	}
	return &mockShareLinkRepo{
		resolveFn: func(context.Context, []byte, time.Time) (*model.ShareLink, error) {
			return &model.ShareLink{ID: shareLinkID, ProjectID: validID, Scope: scope, CreatedByUID: by, OwnerDisplayName: "Victor"}, nil
		},
	}
}

func TestShareLinkService_Create(t *testing.T) {
	ctx := context.Background()
	var gotHash []byte
	var gotMax int
	links := &mockShareLinkRepo{createFn: func(_ context.Context, l *model.ShareLink, hash []byte, maxActive int) (*model.ShareLink, error) {
		gotHash, gotMax = hash, maxActive
		return l, nil
	}}
	link, token, err := shareSvc(links, nil, nil).CreateShareLink(ctx, owner, application.CreateShareLinkInput{
		ProjectID: validID, Scope: model.ShareScopePlan, Duration: model.ShareDurationSevenDays, Note: " jefe ",
	})
	if err != nil {
		t.Fatalf("CreateShareLink: %v", err)
	}
	sum := sha256.Sum256([]byte(token))
	if !model.LooksLikeShareToken(token) || !bytes.Equal(gotHash, sum[:]) || gotMax != model.MaxActiveShareLinksPerProject {
		t.Errorf("token %q, hash %x, max %d", token, gotHash, gotMax)
	}
	if link.ProjectID != validID || link.Note != "jefe" || link.CreatedByUID != owner ||
		link.ExpiresAt == nil || !link.ExpiresAt.Equal(clockNow.AddDate(0, 0, 7)) {
		t.Errorf("link: got %+v", link)
	}

	// Dos tokens nunca se repiten.
	_, other, _ := shareSvc(links, nil, nil).CreateShareLink(ctx, owner, application.CreateShareLinkInput{
		ProjectID: validID, Scope: model.ShareScopePlan, Duration: model.ShareDurationNever, Note: "x",
	})
	if other == token {
		t.Error("dos enlaces con el mismo token")
	}

	// Validaciones y proyecto ajeno: no llegan al repo.
	fail := &mockShareLinkRepo{createFn: func(context.Context, *model.ShareLink, []byte, int) (*model.ShareLink, error) {
		t.Fatal("no debería llegar al repo")
		return nil, nil
	}}
	bad := []struct {
		name string
		uid  string
		in   application.CreateShareLinkInput
		want func(error) bool
	}{
		{"ajeno", "u2", application.CreateShareLinkInput{ProjectID: validID, Scope: model.ShareScopePlan, Duration: model.ShareDurationNever, Note: "x"}, domainerrors.IsNotFound},
		{"sin alcance", owner, application.CreateShareLinkInput{ProjectID: validID, Duration: model.ShareDurationNever, Note: "x"}, domainerrors.IsValidation},
		{"sin vencimiento elegido", owner, application.CreateShareLinkInput{ProjectID: validID, Scope: model.ShareScopePlan, Note: "x"}, domainerrors.IsValidation},
		{"sin nota", owner, application.CreateShareLinkInput{ProjectID: validID, Scope: model.ShareScopePlan, Duration: model.ShareDurationNever, Note: "  "}, domainerrors.IsValidation},
		{"nombre largo", owner, application.CreateShareLinkInput{ProjectID: validID, Scope: model.ShareScopePlan, Duration: model.ShareDurationNever, Note: "x", OwnerDisplayName: strings.Repeat("v", 41)}, domainerrors.IsValidation},
	}
	for _, c := range bad {
		if _, _, err := shareSvc(fail, nil, nil).CreateShareLink(ctx, c.uid, c.in); !c.want(err) {
			t.Errorf("%s: got %v", c.name, err)
		}
	}

	// Error del repo: se propaga.
	boom := &mockShareLinkRepo{createFn: func(context.Context, *model.ShareLink, []byte, int) (*model.ShareLink, error) {
		return nil, domainerrors.InternalError("db down", nil)
	}}
	_, _, err = shareSvc(boom, nil, nil).CreateShareLink(ctx, owner, application.CreateShareLinkInput{
		ProjectID: validID, Scope: model.ShareScopePanel, Duration: model.ShareDurationNever, Note: "x",
	})
	if !domainerrors.IsInternal(err) {
		t.Errorf("repo: got %v", err)
	}
}

func TestShareLinkService_List(t *testing.T) {
	ctx := context.Background()
	links := &mockShareLinkRepo{listFn: func(_ context.Context, projectID string) ([]*model.ShareLink, error) {
		if projectID != validID {
			t.Errorf("projectID: got %q", projectID)
		}
		return []*model.ShareLink{{ID: shareLinkID}}, nil
	}}
	got, err := shareSvc(links, nil, nil).ListShareLinks(ctx, owner, validID)
	if err != nil || len(got) != 1 {
		t.Errorf("listar: %v %+v", err, got)
	}
	if _, err := shareSvc(links, nil, nil).ListShareLinks(ctx, "u2", validID); !domainerrors.IsNotFound(err) {
		t.Errorf("ajeno: got %v", err)
	}
	if _, err := shareSvc(links, nil, nil).ListShareLinks(ctx, "", validID); err == nil {
		t.Error("sin usuario: esperaba error")
	}
	boom := &mockShareLinkRepo{listFn: func(context.Context, string) ([]*model.ShareLink, error) {
		return nil, domainerrors.InternalError("db down", nil)
	}}
	if _, err := shareSvc(boom, nil, nil).ListShareLinks(ctx, owner, validID); !domainerrors.IsInternal(err) {
		t.Errorf("repo: got %v", err)
	}
}

func TestShareLinkService_Revoke(t *testing.T) {
	ctx := context.Background()
	var got []string
	links := &mockShareLinkRepo{
		revokeFn: func(_ context.Context, projectID, id string, at time.Time) (*model.ShareLink, error) {
			got = append(got, projectID, id)
			if id != shareLinkID {
				return nil, domainerrors.NotFoundError("share link not found", nil)
			}
			return &model.ShareLink{ID: id, ProjectID: projectID, RevokedAt: &at}, nil
		},
	}
	link, err := shareSvc(links, nil, nil).RevokeShareLink(ctx, owner, validID, strings.ToUpper(shareLinkID))
	if err != nil || link.RevokedAt == nil || !link.RevokedAt.Equal(clockNow) || got[0] != validID || got[1] != shareLinkID {
		t.Errorf("revocar: %v %+v %v", err, link, got)
	}
	if _, err := shareSvc(links, nil, nil).RevokeShareLink(ctx, owner, validID, "00000000-0000-4000-8000-0000000000c9"); !domainerrors.IsNotFound(err) {
		t.Errorf("inexistente: got %v", err)
	}
	if _, err := shareSvc(links, nil, nil).RevokeShareLink(ctx, owner, validID, "x"); !domainerrors.IsValidation(err) {
		t.Errorf("id inválido: got %v", err)
	}
	calls := len(got)
	if _, err := shareSvc(links, nil, nil).RevokeShareLink(ctx, "u2", validID, shareLinkID); !domainerrors.IsNotFound(err) || len(got) != calls {
		t.Errorf("proyecto ajeno: got %v (llegó al repo: %v)", err, len(got) != calls)
	}
}

func TestShareLinkService_Resolve(t *testing.T) {
	ctx := context.Background()

	// Un token con otra forma no llega a la base.
	never := &mockShareLinkRepo{resolveFn: func(context.Context, []byte, time.Time) (*model.ShareLink, error) {
		t.Fatal("no debería buscar un token mal formado")
		return nil, nil
	}}
	if _, err := shareSvc(never, nil, nil).GetSharedProject(ctx, "tka_"+strings.Repeat("A", 43)); !domainerrors.IsNotFound(err) {
		t.Errorf("mal formado: got %v", err)
	}

	// El repo no lo encuentra: el mismo NotFound, sin el detalle del repo.
	missing := &mockShareLinkRepo{resolveFn: func(_ context.Context, _ []byte, now time.Time) (*model.ShareLink, error) {
		if !now.Equal(clockNow) {
			t.Errorf("now: got %v", now)
		}
		return nil, domainerrors.NotFoundError("share link not found", map[string]any{"detalle": "vencido"})
	}}
	_, err := shareSvc(missing, nil, nil).GetSharedProject(ctx, validShareToken)
	var de *domainerrors.DomainError
	if !domainerrors.IsNotFound(err) || !errors.As(err, &de) || len(de.Details) != 0 {
		t.Errorf("desconocido: got %v (el detalle del repo no sale)", err)
	}

	// Un error de la base no se disfraza de 404.
	down := &mockShareLinkRepo{resolveFn: func(context.Context, []byte, time.Time) (*model.ShareLink, error) {
		return nil, domainerrors.InternalError("db down", nil)
	}}
	if _, _, _, err := shareSvc(down, nil, nil).ListSharedIssues(ctx, validShareToken, port.PageFilter{}); !domainerrors.IsInternal(err) {
		t.Errorf("base caída: got %v", err)
	}

	// Enlace activo cuyo creador ya no tiene el proyecto en sus espacios:
	// NotFound (en la rebanada 3, perder el acceso apaga sus enlaces).
	if _, err := shareSvc(resolvesTo(model.ShareScopePanel, "u2"), nil, nil).GetSharedProject(ctx, validShareToken); !domainerrors.IsNotFound(err) {
		t.Errorf("creador sin acceso: got %v", err)
	}
}

func TestShareLinkService_GetSharedProject(t *testing.T) {
	ctx := context.Background()
	statuses := &mockStatusRepo{listFn: func(context.Context, string) ([]*model.Status, error) {
		return []*model.Status{
			{ID: "s1", Category: model.IssueStateTodo},
			{ID: "s2", Category: model.IssueStateCanceled},
			{ID: "s3", Category: model.IssueStateDone},
		}, nil
	}}
	labels := &mockLabelRepo{listFn: func(context.Context, string) ([]*model.Label, error) {
		return []*model.Label{{ID: "l1", Name: "perfil/escuela"}}, nil
	}}
	view, err := shareSvcWith(resolvesTo(model.ShareScopePlan), nil, nil, statuses, labels).GetSharedProject(ctx, validShareToken)
	if err != nil || view.Project.ID != validID || view.Scope != model.ShareScopePlan || len(view.Labels) != 1 {
		t.Fatalf("GetSharedProject: %v %+v", err, view)
	}
	if len(view.Statuses) != 2 || view.Statuses[0].ID != "s1" || view.Statuses[1].ID != "s3" {
		t.Errorf("estados sin los descartados: got %+v", view.Statuses)
	}
	boom := &mockStatusRepo{listFn: func(context.Context, string) ([]*model.Status, error) {
		return nil, domainerrors.InternalError("db down", nil)
	}}
	if _, err := shareSvcWith(resolvesTo(model.ShareScopePlan), nil, nil, boom, labels).GetSharedProject(ctx, validShareToken); !domainerrors.IsInternal(err) {
		t.Errorf("repo: got %v", err)
	}
}

func TestShareLinkService_ListSharedIssues(t *testing.T) {
	ctx := context.Background()
	var gotFilter port.IssueFilter
	issues := &mockIssueRepo{listFn: func(_ context.Context, f port.IssueFilter) (*port.IssuePage, error) {
		gotFilter = f
		return &port.IssuePage{Issues: []*model.Issue{
			{Number: 1, Title: "a", Description: "**Qué espera:** el visto bueno\n## X", AssigneeActorID: personID},
			{Number: 2, Title: "b", AssigneeActorID: personID},
			{Number: 3, Title: "c", AssigneeActorID: "00000000-0000-4000-8000-0000000000a9"},
		}, NextCursor: "next", HasNext: true}, nil
	}}
	gets := 0
	actors := &mockActorRepo{getFn: func(_ context.Context, id string) (*model.Actor, error) {
		gets++
		if id == personID {
			return &model.Actor{ID: id, Kind: model.ActorKindPerson, UserUID: owner}, nil
		}
		return &model.Actor{ID: id, Kind: model.ActorKindPerson, UserUID: "otra-persona"}, nil
	}}
	out, next, hasNext, err := shareSvc(resolvesTo(model.ShareScopePlan), issues, actors).
		ListSharedIssues(ctx, validShareToken, port.PageFilter{Cursor: "c1", Limit: 500})
	if err != nil {
		t.Fatalf("ListSharedIssues: %v", err)
	}
	if gotFilter.ProjectID != validID || gotFilter.Limit != application.MaxPageLimit || gotFilter.Cursor != "c1" ||
		len(gotFilter.States) != 3 || gotFilter.Order != model.IssueOrderRank {
		t.Errorf("filtro: got %+v", gotFilter)
	}
	for _, s := range gotFilter.States {
		if s == model.IssueStateCanceled {
			t.Error("pidió los descartados")
		}
	}
	if next != "next" || !hasNext || len(out) != 3 {
		t.Fatalf("página: %q %v %d", next, hasNext, len(out))
	}
	if out[0].Description != "" || out[0].WaitingNote != "el visto bueno" {
		t.Errorf("plan: %+v", out[0])
	}
	if out[0].Assignee.DisplayName != "Victor" || out[2].Assignee.DisplayName != "" {
		t.Errorf("personas: dueño %+v, otra %+v", out[0].Assignee, out[2].Assignee)
	}
	if gets != 2 {
		t.Errorf("actores leídos: got %d, want 2 (caché por request)", gets)
	}

	boom := &mockIssueRepo{listFn: func(context.Context, port.IssueFilter) (*port.IssuePage, error) {
		return nil, domainerrors.InternalError("db down", nil)
	}}
	if _, _, _, err := shareSvc(resolvesTo(model.ShareScopePanel), boom, nil).ListSharedIssues(ctx, validShareToken, port.PageFilter{}); !domainerrors.IsInternal(err) {
		t.Errorf("repo: got %v", err)
	}
}

func TestShareLinkService_GetSharedIssue(t *testing.T) {
	ctx := context.Background()
	issues := &mockIssueRepo{getByNumberFn: func(_ context.Context, _ string, number int32) (*model.Issue, error) {
		switch number {
		case 1:
			return &model.Issue{Number: 1, Title: "a", Description: "**Qué espera:** x\nDetalle"}, nil
		case 2:
			return &model.Issue{Number: 2, State: model.IssueStateCanceled}, nil
		case 3:
			return nil, domainerrors.InternalError("db down", nil)
		}
		return nil, domainerrors.NotFoundError("issue not found", nil)
	}}
	panel := shareSvc(resolvesTo(model.ShareScopePanel), issues, nil)
	got, err := panel.GetSharedIssue(ctx, validShareToken, 1)
	if err != nil || got.Description == "" || got.WaitingNote != "" {
		t.Errorf("panel: %v %+v", err, got)
	}
	for _, n := range []int32{0, -1, 2, 99} {
		if _, err := panel.GetSharedIssue(ctx, validShareToken, n); !domainerrors.IsNotFound(err) {
			t.Errorf("número %d: got %v", n, err)
		}
	}
	if _, err := panel.GetSharedIssue(ctx, validShareToken, 3); !domainerrors.IsInternal(err) {
		t.Errorf("repo: got %v", err)
	}
	noLookup := &mockIssueRepo{getByNumberFn: func(context.Context, string, int32) (*model.Issue, error) {
		t.Error("con alcance plan no se busca el issue")
		return nil, nil
	}}
	if _, err := shareSvc(resolvesTo(model.ShareScopePlan), noLookup, nil).GetSharedIssue(ctx, validShareToken, 1); !domainerrors.IsNotFound(err) {
		t.Errorf("plan: got %v", err)
	}
}

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
	if issues == nil {
		issues = &mockIssueRepo{}
	}
	if actors == nil {
		actors = &mockActorRepo{}
	}
	return application.NewShareLinkService(personalWorkspaces(), ownedProjects(), links, issues,
		&mockStatusRepo{}, &mockLabelRepo{}, actors).WithClock(func() time.Time { return clockNow })
}

// resolvesTo es un repo cuyo Resolve devuelve un enlace activo del proyecto
// validID con ese alcance.
func resolvesTo(scope model.ShareScope) *mockShareLinkRepo {
	return &mockShareLinkRepo{
		resolveFn: func(context.Context, []byte, time.Time) (*model.ResolvedShareLink, error) {
			return &model.ResolvedShareLink{
				Link:        &model.ShareLink{ID: shareLinkID, ProjectID: validID, Scope: scope, CreatedByUID: owner, OwnerDisplayName: "Victor"},
				WorkspaceID: ownerWorkspace,
			}, nil
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

	// Proyecto ajeno: NotFound antes de tocar el repo.
	fail := &mockShareLinkRepo{createFn: func(context.Context, *model.ShareLink, []byte, int) (*model.ShareLink, error) {
		t.Fatal("no debería llegar al repo")
		return nil, nil
	}}
	_, _, err = shareSvc(fail, nil, nil).CreateShareLink(ctx, "u2", application.CreateShareLinkInput{
		ProjectID: validID, Scope: model.ShareScopePlan, Duration: model.ShareDurationNever, Note: "x",
	})
	if !domainerrors.IsNotFound(err) {
		t.Errorf("ajeno: got %v", err)
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

func TestShareLinkService_Revoke(t *testing.T) {
	ctx := context.Background()
	revoked := clockNow.Add(-time.Hour)
	calls := 0
	links := &mockShareLinkRepo{
		getFn: func(_ context.Context, projectID, id string) (*model.ShareLink, error) {
			if id != shareLinkID {
				return nil, domainerrors.NotFoundError("share link not found", nil)
			}
			return &model.ShareLink{ID: id, ProjectID: projectID, RevokedAt: &revoked}, nil
		},
		revokeFn: func(context.Context, string, string, time.Time) (*model.ShareLink, error) {
			calls++
			return nil, nil
		},
	}
	got, err := shareSvc(links, nil, nil).RevokeShareLink(ctx, owner, validID, strings.ToUpper(shareLinkID))
	if err != nil || got.RevokedAt == nil || calls != 0 {
		t.Errorf("ya revocado: %v %+v, revoke llamado %d veces", err, got, calls)
	}
	if _, err := shareSvc(links, nil, nil).RevokeShareLink(ctx, owner, validID, "00000000-0000-4000-8000-0000000000c9"); !domainerrors.IsNotFound(err) {
		t.Errorf("inexistente: got %v", err)
	}
	if _, err := shareSvc(links, nil, nil).RevokeShareLink(ctx, owner, validID, "x"); !domainerrors.IsValidation(err) {
		t.Errorf("id inválido: got %v", err)
	}
}

func TestShareLinkService_Resolve(t *testing.T) {
	ctx := context.Background()

	// Un token con otra forma no llega a la base.
	never := &mockShareLinkRepo{resolveFn: func(context.Context, []byte, time.Time) (*model.ResolvedShareLink, error) {
		t.Fatal("no debería buscar un token mal formado")
		return nil, nil
	}}
	if _, err := shareSvc(never, nil, nil).GetSharedProject(ctx, "tka_"+strings.Repeat("A", 43)); !domainerrors.IsNotFound(err) {
		t.Errorf("mal formado: got %v", err)
	}

	// El repo no lo encuentra: el mismo NotFound.
	missing := &mockShareLinkRepo{resolveFn: func(_ context.Context, _ []byte, now time.Time) (*model.ResolvedShareLink, error) {
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
	down := &mockShareLinkRepo{resolveFn: func(context.Context, []byte, time.Time) (*model.ResolvedShareLink, error) {
		return nil, domainerrors.InternalError("db down", nil)
	}}
	if _, _, _, err := shareSvc(down, nil, nil).ListSharedIssues(ctx, validShareToken, port.PageFilter{}); !domainerrors.IsInternal(err) {
		t.Errorf("base caída: got %v", err)
	}

	// Enlace activo de un proyecto que ya no está en su espacio: NotFound.
	moved := &mockShareLinkRepo{resolveFn: func(context.Context, []byte, time.Time) (*model.ResolvedShareLink, error) {
		return &model.ResolvedShareLink{Link: &model.ShareLink{ProjectID: validID, Scope: model.ShareScopePanel}, WorkspaceID: "ws-otro"}, nil
	}}
	if _, err := shareSvc(moved, nil, nil).GetSharedProject(ctx, validShareToken); !domainerrors.IsNotFound(err) {
		t.Errorf("proyecto movido: got %v", err)
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
}

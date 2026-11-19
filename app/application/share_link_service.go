package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"time"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// ShareLinkService implementa los enlaces de solo lectura (issue 009):
//
//   - Gestión (CreateShareLink, ListShareLinks, RevokeShareLink): solo el
//     dueño del proyecto (el proyecto es de su espacio). Un proyecto o un
//     enlace ajeno responde NotFound.
//   - Lectura pública por token (GetSharedProject, ListSharedIssues,
//     GetSharedIssue): sin identidad. El proyecto sale del enlace, nunca del
//     request. Un token mal formado, desconocido, vencido o revocado responde
//     siempre el mismo NotFound (errShareLinkNotFound).
type ShareLinkService struct {
	guard    projectGuard
	links    port.ShareLinkRepository
	issues   port.IssueRepository
	statuses port.StatusRepository
	labels   port.LabelRepository
	actors   port.ActorRepository
	now      func() time.Time
}

// NewShareLinkService construye el servicio.
func NewShareLinkService(
	workspaceRepo port.WorkspaceRepository,
	projectRepo port.ProjectRepository,
	links port.ShareLinkRepository,
	issues port.IssueRepository,
	statuses port.StatusRepository,
	labels port.LabelRepository,
	actors port.ActorRepository,
) *ShareLinkService {
	return &ShareLinkService{
		guard:    projectGuard{workspaces: workspaces{repo: workspaceRepo}, projects: projectRepo},
		links:    links,
		issues:   issues,
		statuses: statuses,
		labels:   labels,
		actors:   actors,
		now:      utcNow,
	}
}

// WithClock reemplaza el reloj (tests).
func (s *ShareLinkService) WithClock(now func() time.Time) *ShareLinkService {
	s.now = now
	return s
}

// CreateShareLinkInput son los datos que acepta CreateShareLink.
type CreateShareLinkInput struct {
	ProjectID        string
	Scope            model.ShareScope
	Duration         model.ShareDuration
	Note             string
	OwnerDisplayName string
}

// CreateShareLink crea un enlace del proyecto. Devuelve el enlace y el token
// en claro, que no se guarda.
func (s *ShareLinkService) CreateShareLink(ctx context.Context, uid string, in CreateShareLinkInput) (*model.ShareLink, string, error) {
	if _, err := s.guard.ensure(ctx, uid, in.ProjectID); err != nil {
		return nil, "", err
	}
	if !in.Scope.IsValid() {
		return nil, "", domainerrors.ValidationError("scope is required", map[string]any{"field": "scope"})
	}
	note, err := model.NormalizeShareNote(in.Note)
	if err != nil {
		return nil, "", err
	}
	ownerName, err := model.NormalizeShareOwnerName(in.OwnerDisplayName)
	if err != nil {
		return nil, "", err
	}
	now := s.now()
	expires, err := model.ShareLinkExpiry(now, in.Duration)
	if err != nil {
		return nil, "", err
	}
	secret, err := newShareTokenSecret()
	if err != nil {
		return nil, "", err
	}
	link, err := s.links.Create(ctx, &model.ShareLink{
		ID:               uuid.NewString(),
		ProjectID:        canonical(in.ProjectID),
		Scope:            in.Scope,
		Note:             note,
		OwnerDisplayName: ownerName,
		CreatedByUID:     uid,
		CreatedAt:        now,
		ExpiresAt:        expires,
	}, HashToken(secret), model.MaxActiveShareLinksPerProject)
	if err != nil {
		return nil, "", err
	}
	return link, secret, nil
}

// ListShareLinks devuelve los enlaces del proyecto, los más nuevos primero.
func (s *ShareLinkService) ListShareLinks(ctx context.Context, uid, projectID string) ([]*model.ShareLink, error) {
	if _, err := s.guard.ensure(ctx, uid, projectID); err != nil {
		return nil, err
	}
	return s.links.List(ctx, canonical(projectID))
}

// RevokeShareLink revoca un enlace del proyecto. No se deshace; uno ya
// revocado se devuelve tal cual.
func (s *ShareLinkService) RevokeShareLink(ctx context.Context, uid, projectID, linkID string) (*model.ShareLink, error) {
	if _, err := s.guard.ensure(ctx, uid, projectID); err != nil {
		return nil, err
	}
	linkID, err := canonicalUUID("share_link_id", linkID)
	if err != nil {
		return nil, err
	}
	projectID = canonical(projectID)
	// Revoke es idempotente y da NotFound si el enlace no es del proyecto.
	return s.links.Revoke(ctx, projectID, linkID, s.now())
}

// errShareLinkNotFound es la única respuesta a un token que no sirve: no dice
// si el enlace existió, venció o se revocó.
func errShareLinkNotFound() error {
	return domainerrors.NotFoundError("share link not found", nil)
}

// sharedProject es un enlace resuelto con su proyecto.
type sharedProject struct {
	link    *model.ShareLink
	project *model.Project
}

// resolve resuelve el token a su enlace activo y su proyecto. El enlace vale
// mientras el proyecto siga en uno de los espacios de quien lo creó (si lo
// pierde, en la rebanada 3, el enlace deja de andar). Un token que no sirve
// (por la razón que sea) da errShareLinkNotFound; un error de la base sale
// como error (el gateway responde 5xx, no 404).
func (s *ShareLinkService) resolve(ctx context.Context, token string) (*sharedProject, error) {
	if !model.LooksLikeShareToken(token) {
		return nil, errShareLinkNotFound()
	}
	link, err := s.links.Resolve(ctx, HashToken(token), s.now())
	if domainerrors.IsNotFound(err) {
		return nil, errShareLinkNotFound()
	}
	if err != nil {
		return nil, err
	}
	workspaceIDs, err := s.guard.workspaces.ids(ctx, link.CreatedByUID)
	if err != nil {
		return nil, err
	}
	project, err := s.guard.projects.GetByID(ctx, workspaceIDs, link.ProjectID)
	if domainerrors.IsNotFound(err) {
		return nil, errShareLinkNotFound()
	}
	if err != nil {
		return nil, err
	}
	return &sharedProject{link: link, project: project}, nil
}

// GetSharedProject devuelve el proyecto del enlace, su alcance, sus estados
// (sin los de la categoría CANCELED: los descartados no salen) y sus
// etiquetas.
func (s *ShareLinkService) GetSharedProject(ctx context.Context, token string) (*model.SharedProjectView, error) {
	sp, err := s.resolve(ctx, token)
	if err != nil {
		return nil, err
	}
	statuses, err := s.statuses.List(ctx, sp.project.ID)
	if err != nil {
		return nil, err
	}
	visible := make([]*model.Status, 0, len(statuses))
	for _, st := range statuses {
		if st.Category != model.IssueStateCanceled {
			visible = append(visible, st)
		}
	}
	labels, err := s.labels.List(ctx, sp.project.ID)
	if err != nil {
		return nil, err
	}
	return &model.SharedProjectView{Project: sp.project, Scope: sp.link.Scope, Statuses: visible, Labels: labels}, nil
}

// sharedStates son las categorías que se ven por un enlace: todas menos
// CANCELED.
var sharedStates = []model.IssueState{model.IssueStateTodo, model.IssueStateInProgress, model.IssueStateDone}

// ListSharedIssues pagina los issues del proyecto del enlace por rank, sin
// los descartados.
func (s *ShareLinkService) ListSharedIssues(ctx context.Context, token string, page port.PageFilter) ([]*model.SharedIssue, string, bool, error) {
	sp, err := s.resolve(ctx, token)
	if err != nil {
		return nil, "", false, err
	}
	res, err := s.issues.List(ctx, port.IssueFilter{
		ProjectID: sp.project.ID,
		States:    sharedStates,
		Order:     model.IssueOrderRank,
		Cursor:    page.Cursor,
		Limit:     normalizeLimit(page.Limit),
	})
	if err != nil {
		return nil, "", false, err
	}
	assignees := s.assigneeResolver(sp.link)
	out := make([]*model.SharedIssue, 0, len(res.Issues))
	for _, it := range res.Issues {
		shared, err := s.toShared(ctx, sp.link, it, assignees)
		if err != nil {
			return nil, "", false, err
		}
		out = append(out, shared)
	}
	return out, res.NextCursor, res.HasNext, nil
}

// GetSharedIssue devuelve un issue del proyecto del enlace. Solo con alcance
// panel: con plan, NotFound (igual que un issue que no existe o está
// descartado).
func (s *ShareLinkService) GetSharedIssue(ctx context.Context, token string, number int32) (*model.SharedIssue, error) {
	sp, err := s.resolve(ctx, token)
	if err != nil {
		return nil, err
	}
	notFound := domainerrors.NotFoundError("issue not found", map[string]any{"number": number})
	if sp.link.Scope != model.ShareScopePanel || number <= 0 {
		return nil, notFound
	}
	it, err := s.issues.GetByNumber(ctx, sp.project.ID, number)
	if domainerrors.IsNotFound(err) {
		return nil, notFound
	}
	if err != nil {
		return nil, err
	}
	if it.State == model.IssueStateCanceled {
		return nil, notFound
	}
	return s.toShared(ctx, sp.link, it, s.assigneeResolver(sp.link))
}

// toShared arma la vista pública de un issue según el alcance del enlace:
// con panel, la descripción; con plan, solo la línea «Qué espera».
func (s *ShareLinkService) toShared(ctx context.Context, link *model.ShareLink, it *model.Issue, assignees func(context.Context, string) (*model.SharedActor, error)) (*model.SharedIssue, error) {
	assignee, err := assignees(ctx, it.AssigneeActorID)
	if err != nil {
		return nil, err
	}
	out := &model.SharedIssue{
		Number:    it.Number,
		Title:     it.Title,
		State:     it.State,
		Priority:  it.Priority,
		StatusID:  it.StatusID,
		LabelIDs:  append([]string(nil), it.LabelIDs...),
		Assignee:  assignee,
		CreatedAt: it.CreatedAt,
		UpdatedAt: it.UpdatedAt,
	}
	if link.Scope == model.ShareScopePanel {
		out.Description = it.Description
	} else {
		out.WaitingNote = model.WaitingNote(it.Description)
	}
	return out, nil
}

// assigneeResolver devuelve una función que resuelve un asignado a su vista
// pública, con caché por request (en una página se repiten pocos actores).
// Una persona se muestra con el owner_display_name del enlace si es quien lo
// creó; si no, sin nombre (el servicio no conoce nombres y uid/email nunca
// salen).
func (s *ShareLinkService) assigneeResolver(link *model.ShareLink) func(context.Context, string) (*model.SharedActor, error) {
	cache := map[string]*model.SharedActor{}
	return func(ctx context.Context, actorID string) (*model.SharedActor, error) {
		if actorID == "" {
			return nil, nil
		}
		if a, ok := cache[actorID]; ok {
			return a, nil
		}
		actor, err := s.actors.Get(ctx, actorID)
		if domainerrors.IsNotFound(err) {
			cache[actorID] = nil
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		shared := &model.SharedActor{Kind: actor.Kind}
		switch actor.Kind {
		case model.ActorKindAgent:
			shared.DisplayName, shared.Tool = actor.DisplayName, actor.Tool
		case model.ActorKindPerson:
			if actor.UserUID == link.CreatedByUID {
				shared.DisplayName = link.OwnerDisplayName
			}
		}
		cache[actorID] = shared
		return shared, nil
	}
}

// newShareTokenSecret genera "tks_" + 32 bytes aleatorios en base64url.
func newShareTokenSecret() (string, error) {
	b := make([]byte, model.ShareTokenSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", domainerrors.InternalError("failed to generate share token", map[string]any{"cause": err.Error()})
	}
	return model.ShareTokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

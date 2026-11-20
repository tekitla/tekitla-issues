package application

import (
	"context"
	"time"

	"github.com/google/uuid"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// IntegrationService implementa la integración con GitHub y GitLab (issue
// 016):
//
//   - Gestión (conexiones, repos, links): solo el dueño del proyecto. Un
//     proyecto, conexión, repo o link ajeno responde NotFound.
//   - Webhooks (ReceiveWebhook): sin identidad. La autenticación es la firma
//     (GitHub) o el secreto (GitLab); el proyecto sale del repo conectado,
//     nunca del payload.
//
// Solo actualiza links: no crea ni mueve issues (las automatizaciones del ADR
// 0008 son la parte 2), así que un PR fusionado nunca cambia el estado de un
// issue (CU-016-9).
type IntegrationService struct {
	guard  projectGuard
	repo   port.IntegrationRepository
	github port.GitHubApp // nil: GitHub no está habilitado en el ambiente
	now    func() time.Time
}

// NewIntegrationService construye el servicio. github puede ser nil.
func NewIntegrationService(
	workspaceRepo port.WorkspaceRepository,
	projectRepo port.ProjectRepository,
	repo port.IntegrationRepository,
	github port.GitHubApp,
) *IntegrationService {
	return &IntegrationService{
		guard:  projectGuard{workspaces: workspaces{repo: workspaceRepo}, projects: projectRepo},
		repo:   repo,
		github: github,
		now:    utcNow,
	}
}

// WithClock reemplaza el reloj (tests).
func (s *IntegrationService) WithClock(now func() time.Time) *IntegrationService {
	s.now = now
	return s
}

// githubHost es el host de las conexiones de GitHub (no hay GitHub
// Enterprise).
const githubHost = "github.com"

func errGitHubNotEnabled() error {
	return domainerrors.FailedPreconditionError("GitHub is not enabled in this environment", nil).WithReason("GITHUB_NOT_ENABLED")
}

// ConnectionsView es la respuesta de ListConnections.
type ConnectionsView struct {
	Connections      []*model.Connection
	GitHubInstallURL string
	GitLabEnabled    bool
}

// ListConnections devuelve las conexiones del proyecto y qué proveedores
// están habilitados.
func (s *IntegrationService) ListConnections(ctx context.Context, uid, projectID string) (*ConnectionsView, error) {
	if _, err := s.guard.ensure(ctx, uid, projectID); err != nil {
		return nil, err
	}
	conns, err := s.repo.ListConnections(ctx, canonical(projectID))
	if err != nil {
		return nil, err
	}
	view := &ConnectionsView{Connections: conns}
	if s.github != nil {
		view.GitHubInstallURL = s.github.InstallURL()
	}
	return view, nil
}

// ConnectInput son los datos de ConnectOrganization.
type ConnectInput struct {
	ProjectID string
	Provider  model.RepoProvider
	// GitHub.
	InstallationID int64
	Scope          model.ConnectionScope
	Topic          string
	Selected       []string
}

// ConnectOrganization conecta una organización de GitHub con la App ya
// instalada. GitLab espera la decisión de Victor sobre el GitLab de Round
// (plan §3.2): FailedPrecondition GITLAB_NOT_ENABLED.
func (s *IntegrationService) ConnectOrganization(ctx context.Context, uid string, in ConnectInput) (*model.Connection, []*model.Repository, error) {
	if _, err := s.guard.ensure(ctx, uid, in.ProjectID); err != nil {
		return nil, nil, err
	}
	switch in.Provider {
	case model.RepoProviderGitHub:
	case model.RepoProviderGitLab:
		return nil, nil, domainerrors.FailedPreconditionError("GitLab is not enabled in this environment", nil).WithReason("GITLAB_NOT_ENABLED")
	default:
		return nil, nil, domainerrors.ValidationError("github or gitlab is required", map[string]any{"field": "provider"})
	}
	if s.github == nil {
		return nil, nil, errGitHubNotEnabled()
	}
	if in.InstallationID <= 0 {
		return nil, nil, domainerrors.ValidationError("installation_id is required", map[string]any{"field": "github.installation_id"})
	}
	topic, selected, err := model.NormalizeScope(in.Scope, in.Topic, in.Selected)
	if err != nil {
		return nil, nil, err
	}
	// El installation_id viene de una URL: se valida con la App (que es
	// privada, así que solo existe en la org tekitla).
	owner, err := s.github.Installation(ctx, in.InstallationID)
	if domainerrors.IsNotFound(err) {
		return nil, nil, domainerrors.FailedPreconditionError("the GitHub App has no such installation", nil).
			WithReason("GITHUB_INSTALLATION_NOT_FOUND")
	}
	if err != nil {
		return nil, nil, err
	}
	remote, err := s.github.ListRepos(ctx, in.InstallationID)
	if err != nil {
		return nil, nil, err
	}
	conn := &model.Connection{
		ID:             uuid.NewString(),
		ProjectID:      canonical(in.ProjectID),
		Provider:       model.RepoProviderGitHub,
		Host:           githubHost,
		Owner:          owner,
		InstallationID: in.InstallationID,
		Scope:          in.Scope,
		Topic:          topic,
		Selected:       selected,
		CreatedByUID:   uid,
		CreatedAt:      s.now(),
	}
	return s.repo.CreateConnection(ctx, conn, nil, model.SelectRepos(remote, in.Scope, topic, selected), model.MaxConnectionsPerProject)
}

// UpdateConnectionInput son los datos de UpdateConnection.
type UpdateConnectionInput struct {
	ProjectID    string
	ConnectionID string
	Scope        model.ConnectionScope
	Topic        string
	Selected     []string
}

// UpdateConnection cambia el alcance y vuelve a listar los repos.
func (s *IntegrationService) UpdateConnection(ctx context.Context, uid string, in UpdateConnectionInput) (*model.Connection, []*model.Repository, error) {
	conn, _, err := s.connection(ctx, uid, in.ProjectID, in.ConnectionID)
	if err != nil {
		return nil, nil, err
	}
	topic, selected, err := model.NormalizeScope(in.Scope, in.Topic, in.Selected)
	if err != nil {
		return nil, nil, err
	}
	remote, err := s.remoteRepos(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	conn.Scope, conn.Topic, conn.Selected = in.Scope, topic, selected
	return s.repo.ReplaceRepositories(ctx, conn, model.SelectRepos(remote, in.Scope, topic, selected))
}

// DisconnectOrganization borra la conexión, sus repos y sus links.
func (s *IntegrationService) DisconnectOrganization(ctx context.Context, uid, projectID, connectionID string) error {
	conn, _, err := s.connection(ctx, uid, projectID, connectionID)
	if err != nil {
		return err
	}
	return s.repo.DeleteConnection(ctx, conn.ProjectID, conn.ID)
}

// SyncResult es la respuesta de SyncRepositories.
type SyncResult struct {
	Connection   *model.Connection
	Repositories []*model.Repository
	LinksUpdated int
}

// SyncRepositories vuelve a listar los repos y liga los PR abiertos de cada
// uno (carga inicial y respaldo de entregas perdidas).
func (s *IntegrationService) SyncRepositories(ctx context.Context, uid, projectID, connectionID string) (*SyncResult, error) {
	conn, project, err := s.connection(ctx, uid, projectID, connectionID)
	if err != nil {
		return nil, err
	}
	remote, err := s.remoteRepos(ctx, conn)
	if err != nil {
		return nil, err
	}
	conn, repos, err := s.repo.ReplaceRepositories(ctx, conn, model.SelectRepos(remote, conn.Scope, conn.Topic, conn.Selected))
	if err != nil {
		return nil, err
	}
	updated := 0
	for _, r := range repos {
		events, err := s.github.ListOpenPRs(ctx, conn.InstallationID, r.Owner, r.Name)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			continue
		}
		_, err = s.repo.Process(ctx, r, nil, func(tx port.LinkTx) error {
			for i := range events {
				n, err := s.apply(ctx, tx, r, project.Key, &events[i])
				if err != nil {
					return err
				}
				updated += n
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return &SyncResult{Connection: conn, Repositories: repos, LinksUpdated: updated}, nil
}

// connection valida el proyecto y devuelve la conexión y el proyecto.
func (s *IntegrationService) connection(ctx context.Context, uid, projectID, connectionID string) (*model.Connection, *model.Project, error) {
	project, _, err := s.guard.project(ctx, uid, projectID)
	if err != nil {
		return nil, nil, err
	}
	connectionID, err = canonicalUUID("connection_id", connectionID)
	if err != nil {
		return nil, nil, err
	}
	conn, err := s.repo.GetConnection(ctx, project.ID, connectionID)
	if err != nil {
		return nil, nil, err
	}
	return conn, project, nil
}

func (s *IntegrationService) remoteRepos(ctx context.Context, conn *model.Connection) ([]model.RemoteRepo, error) {
	if conn.Provider != model.RepoProviderGitHub {
		return nil, domainerrors.FailedPreconditionError("GitLab is not enabled in this environment", nil).WithReason("GITLAB_NOT_ENABLED")
	}
	if s.github == nil {
		return nil, errGitHubNotEnabled()
	}
	return s.github.ListRepos(ctx, conn.InstallationID)
}

// ListRepositories devuelve los repos del proyecto.
func (s *IntegrationService) ListRepositories(ctx context.Context, uid, projectID string) ([]*model.Repository, error) {
	if _, err := s.guard.ensure(ctx, uid, projectID); err != nil {
		return nil, err
	}
	return s.repo.ListRepositories(ctx, canonical(projectID))
}

// ListIssueLinks devuelve los links de un issue (number > 0) o del proyecto.
func (s *IntegrationService) ListIssueLinks(ctx context.Context, uid, projectID string, number int32) ([]*model.IssueLink, error) {
	if _, err := s.guard.ensure(ctx, uid, projectID); err != nil {
		return nil, err
	}
	if number < 0 {
		return nil, domainerrors.ValidationError("issue_number must be >= 0", map[string]any{"field": "issue_number"})
	}
	limit := model.MaxProjectLinks
	if number > 0 {
		limit = model.MaxLinksPerIssue
	}
	return s.repo.ListLinks(ctx, canonical(projectID), number, limit)
}

// LinkIssue liga a mano una rama de un repo del proyecto a un issue.
func (s *IntegrationService) LinkIssue(ctx context.Context, uid, projectID string, number int32, repositoryID, branch string) (*model.IssueLink, error) {
	if _, err := s.guard.ensure(ctx, uid, projectID); err != nil {
		return nil, err
	}
	if err := validateIssueNumber(number); err != nil {
		return nil, err
	}
	repositoryID, err := canonicalUUID("repository_id", repositoryID)
	if err != nil {
		return nil, err
	}
	if !model.ValidBranch(branch) {
		return nil, domainerrors.ValidationError("branch must be 1-255 characters", map[string]any{"field": "branch"})
	}
	projectID = canonical(projectID)
	repo, err := s.repo.GetRepository(ctx, projectID, repositoryID)
	if err != nil {
		return nil, err
	}
	var out *model.IssueLink
	_, err = s.repo.Process(ctx, repo, nil, func(tx port.LinkTx) error {
		ids, err := tx.IssueIDs(ctx, projectID, []int32{number})
		if err != nil {
			return err
		}
		issueID, ok := ids[number]
		if !ok {
			return domainerrors.NotFoundError("issue not found", map[string]any{"number": number})
		}
		existing, err := tx.LinksByBranch(ctx, branch)
		if err != nil {
			return err
		}
		for _, l := range existing {
			if l.IssueID == issueID {
				l.Suppressed = false
				l.UpdatedAt = s.now()
				out = l
				return tx.UpdateLink(ctx, l)
			}
		}
		l, err := s.newLink(ctx, tx, repo, issueID, number, branch, model.LinkOriginManual)
		if err != nil {
			return err
		}
		if l == nil {
			return domainerrors.FailedPreconditionError("issue has the maximum number of links",
				map[string]any{"max": model.MaxLinksPerIssue}).WithReason("ISSUE_LINK_LIMIT")
		}
		// Hereda lo que ya se sabe de la rama (head, PR, CI) de otro link.
		if len(existing) > 0 {
			copyBranchState(l, existing[0])
		}
		l.UpdatedBy = ""
		out = l
		return tx.InsertLink(ctx, l)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UnlinkIssue suprime un link: un evento nuevo de esa rama no lo recrea.
func (s *IntegrationService) UnlinkIssue(ctx context.Context, uid, projectID string, number int32, linkID string) error {
	if _, err := s.guard.ensure(ctx, uid, projectID); err != nil {
		return err
	}
	if err := validateIssueNumber(number); err != nil {
		return err
	}
	linkID, err := canonicalUUID("link_id", linkID)
	if err != nil {
		return err
	}
	projectID = canonical(projectID)
	links, err := s.repo.ListLinks(ctx, projectID, number, model.MaxLinksPerIssue)
	if err != nil {
		return err
	}
	var target *model.IssueLink
	for _, l := range links {
		if l.ID == linkID {
			target = l
		}
	}
	if target == nil {
		return domainerrors.NotFoundError("link not found", nil)
	}
	_, err = s.repo.Process(ctx, target.Repo, nil, func(tx port.LinkTx) error {
		l, err := tx.GetLink(ctx, target.IssueID, linkID)
		if err != nil {
			return err
		}
		l.Suppressed = true
		l.UpdatedAt = s.now()
		return tx.UpdateLink(ctx, l)
	})
	return err
}

// WebhookOutcome es el resultado de ReceiveWebhook.
type WebhookOutcome int

const (
	WebhookProcessed WebhookOutcome = iota + 1
	WebhookDuplicate
	WebhookIgnored
)

// WebhookInput es una entrega cruda.
type WebhookInput struct {
	Provider   model.RepoProvider
	Event      string
	DeliveryID string
	Signature  string
	Token      string
	Body       []byte
}

// errWebhookUnauthenticated es la única respuesta a una firma o secreto que
// no sirve: no dice por qué.
func errWebhookUnauthenticated() error {
	return domainerrors.UnauthorizedError("invalid webhook signature", nil)
}

// ReceiveWebhook verifica, descarta repetidas y actualiza los links. Una firma
// inválida no escribe nada (CU-016-2).
func (s *IntegrationService) ReceiveWebhook(ctx context.Context, in WebhookInput) (WebhookOutcome, error) {
	if in.Provider != model.RepoProviderGitHub || s.github == nil {
		// GitLab: sin conexiones posibles todavía (plan §3.2), ningún
		// secreto vale.
		return 0, errWebhookUnauthenticated()
	}
	if !s.github.VerifyWebhook(in.Body, in.Signature) {
		return 0, errWebhookUnauthenticated()
	}
	if !model.IsDeliveryID(in.DeliveryID) || in.Event == "" || len(in.Event) > 64 {
		return 0, domainerrors.ValidationError("missing delivery id or event", nil)
	}
	repoID, events, err := s.github.ParseWebhook(in.Event, in.Body)
	if err != nil {
		return 0, err
	}
	if repoID == 0 {
		return WebhookIgnored, nil
	}
	repo, key, err := s.repo.RepositoryByExternalID(ctx, model.RepoProviderGitHub, githubHost, repoID)
	if domainerrors.IsNotFound(err) {
		return WebhookIgnored, nil
	}
	if err != nil {
		return 0, err
	}
	d := &port.Delivery{Provider: in.Provider, ID: in.DeliveryID, Event: in.Event, ReceivedAt: s.now()}
	dup, err := s.repo.Process(ctx, repo, d, func(tx port.LinkTx) error {
		for i := range events {
			if _, err := s.apply(ctx, tx, repo, key, &events[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if dup {
		return WebhookDuplicate, nil
	}
	return WebhookProcessed, nil
}

// apply aplica un evento a los links del repo. Devuelve cuántos links creó
// o cambió.
func (s *IntegrationService) apply(ctx context.Context, tx port.LinkTx, repo *model.Repository, key string, ev *model.RepoEvent) (int, error) {
	now := s.now()
	touch := func(l *model.IssueLink) {
		l.UpdatedBy = model.Truncate(ev.Actor, model.UpdatedByMaxLen)
		l.UpdatedAt = now
	}
	switch ev.Kind {
	case model.RepoEventPush:
		if ev.Deleted {
			links, err := tx.LinksByBranch(ctx, ev.Branch)
			if err != nil {
				return 0, err
			}
			n := 0
			for _, l := range links {
				if l.Suppressed || l.BranchDeleted {
					continue
				}
				l.BranchDeleted = true
				touch(l)
				if err := tx.UpdateLink(ctx, l); err != nil {
					return 0, err
				}
				n++
			}
			return n, nil
		}
		return s.upsertBranch(ctx, tx, repo, ev, model.BranchIssueRefs(key, ev.Branch), nil, func(l *model.IssueLink) {
			setHead(l, ev.HeadSHA)
			l.HeadTitle = ev.HeadTitle
			l.Commits = ev.Commits
			l.BranchDeleted = false
			touch(l)
		})
	case model.RepoEventPR:
		pr := ev.PR
		textRefs := model.TextIssueRefs(key, pr.Title+"\n"+pr.Body)
		return s.upsertBranch(ctx, tx, repo, ev, model.BranchIssueRefs(key, ev.Branch), textRefs, func(l *model.IssueLink) {
			// Un evento más viejo que lo guardado no lo pisa.
			if l.PRNumber == pr.Number && l.PRUpdatedAt != nil && pr.UpdatedAt.Before(*l.PRUpdatedAt) {
				return
			}
			l.PRNumber = pr.Number
			l.PRTitle = model.Truncate(pr.Title, model.PRTitleMaxLen)
			l.PRState = pr.State
			l.ReviewRequested = pr.ReviewRequested
			if !pr.UpdatedAt.IsZero() {
				at := pr.UpdatedAt
				l.PRUpdatedAt = &at
			}
			if l.HeadSHA == "" && model.IsSHA(pr.HeadSHA) {
				setHead(l, pr.HeadSHA)
			}
			touch(l)
		})
	case model.RepoEventReview:
		links, err := tx.LinksByBranch(ctx, ev.Branch)
		if err != nil {
			return 0, err
		}
		n := 0
		for _, l := range links {
			if l.Suppressed || l.PRNumber != ev.PR.Number || l.ReviewRequested == ev.PR.ReviewRequested {
				continue
			}
			l.ReviewRequested = ev.PR.ReviewRequested
			touch(l)
			if err := tx.UpdateLink(ctx, l); err != nil {
				return 0, err
			}
			n++
		}
		return n, nil
	case model.RepoEventCI:
		links, err := tx.LinksForCommit(ctx, ev.HeadSHA, ev.Branch)
		if err != nil {
			return 0, err
		}
		n := 0
		for _, l := range links {
			if l.Suppressed {
				continue
			}
			if l.HeadSHA == "" {
				setHead(l, ev.HeadSHA)
			}
			if l.CISuites == nil {
				l.CISuites = map[string]model.CIStatus{}
			}
			l.CISuites[ev.SuiteID] = ev.CIStatus
			l.CIStatus = model.AggregateCI(l.CISuites)
			touch(l)
			if err := tx.UpdateLink(ctx, l); err != nil {
				return 0, err
			}
			n++
		}
		return n, nil
	}
	return 0, nil
}

// upsertBranch crea los links que faltan para los issues que nombra la rama
// (branchRefs) o el texto del PR (textRefs), y aplica update a todos los
// links sin suprimir de la rama.
func (s *IntegrationService) upsertBranch(ctx context.Context, tx port.LinkTx, repo *model.Repository, ev *model.RepoEvent,
	branchRefs, textRefs []int32, update func(*model.IssueLink)) (int, error) {
	if !model.ValidBranch(ev.Branch) {
		return 0, nil
	}
	existing, err := tx.LinksByBranch(ctx, ev.Branch)
	if err != nil {
		return 0, err
	}
	origin := map[int32]model.LinkOrigin{}
	for _, n := range textRefs {
		origin[n] = model.LinkOriginPRText
	}
	for _, n := range branchRefs {
		origin[n] = model.LinkOriginBranchName
	}
	numbers := make([]int32, 0, len(origin))
	for n := range origin {
		numbers = append(numbers, n)
	}
	ids := map[int32]string{}
	if len(numbers) > 0 {
		if ids, err = tx.IssueIDs(ctx, repo.ProjectID, numbers); err != nil {
			return 0, err
		}
	}
	linked := map[string]bool{}
	for _, l := range existing {
		linked[l.IssueID] = true
	}
	changed := 0
	for _, l := range existing {
		if l.Suppressed {
			continue
		}
		update(l)
		if err := tx.UpdateLink(ctx, l); err != nil {
			return 0, err
		}
		changed++
	}
	for _, n := range numbers {
		issueID, ok := ids[n]
		if !ok || linked[issueID] {
			continue // no existe en el proyecto (CU-016-6) o ya ligado
		}
		l, err := s.newLink(ctx, tx, repo, issueID, n, ev.Branch, origin[n])
		if err != nil {
			return 0, err
		}
		if l == nil {
			continue // tope de links del issue: no falla la entrega
		}
		if len(existing) > 0 {
			copyBranchState(l, existing[0])
		}
		update(l)
		if err := tx.InsertLink(ctx, l); err != nil {
			return 0, err
		}
		linked[issueID] = true
		changed++
	}
	return changed, nil
}

// newLink arma un link nuevo, o nil si el issue ya tiene el tope.
func (s *IntegrationService) newLink(ctx context.Context, tx port.LinkTx, repo *model.Repository, issueID string, number int32, branch string, origin model.LinkOrigin) (*model.IssueLink, error) {
	count, err := tx.CountIssueLinks(ctx, issueID)
	if err != nil {
		return nil, err
	}
	if count >= model.MaxLinksPerIssue {
		return nil, nil
	}
	return &model.IssueLink{
		ID:          uuid.NewString(),
		IssueID:     issueID,
		IssueNumber: number,
		Repo:        repo,
		Branch:      branch,
		PRState:     model.PRStateNone,
		CIStatus:    model.CIStatusNone,
		CISuites:    map[string]model.CIStatus{},
		Origin:      origin,
		UpdatedAt:   s.now(),
	}, nil
}

// setHead cambia el último commit de la rama: el CI del commit anterior ya no
// vale.
func setHead(l *model.IssueLink, sha string) {
	if sha == "" || sha == l.HeadSHA {
		return
	}
	l.HeadSHA = sha
	l.CISuites = map[string]model.CIStatus{}
	l.CIStatus = model.CIStatusNone
}

// copyBranchState copia a un link nuevo lo que ya se sabe de su rama.
func copyBranchState(dst, src *model.IssueLink) {
	dst.HeadSHA, dst.HeadTitle, dst.Commits = src.HeadSHA, src.HeadTitle, src.Commits
	dst.PRNumber, dst.PRTitle, dst.PRState, dst.PRUpdatedAt = src.PRNumber, src.PRTitle, src.PRState, src.PRUpdatedAt
	dst.ReviewRequested, dst.BranchDeleted = src.ReviewRequested, src.BranchDeleted
	dst.CIStatus = src.CIStatus
	dst.CISuites = make(map[string]model.CIStatus, len(src.CISuites))
	for k, v := range src.CISuites {
		dst.CISuites[k] = v
	}
}

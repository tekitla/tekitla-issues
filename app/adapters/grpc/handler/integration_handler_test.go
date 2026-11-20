package handler_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
	"github.com/tekitla/tekitla-issues/app/integrations/github"
)

// Issue 016 de punta a punta: cliente -> bufconn -> handlers -> service ->
// repo en memoria, con una GitHub App falsa que firma y parsea como la real.
// Datos inventados: org acme, repos api/web/sitio, logins dev-prueba.

const (
	testWebhookSecret = "secreto-webhook-de-prueba-0123456789"
	shaA              = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB              = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeGitHub implementa port.GitHubApp: la verificación y el parseo son los
// del adapter real; la API devuelve datos fijos.
type fakeGitHub struct {
	mu            sync.Mutex
	installations map[int64]string
	repos         []model.RemoteRepo
	openPRs       map[string][]model.RepoEvent
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{
		installations: map[int64]string{77: "acme"},
		repos: []model.RemoteRepo{
			{ExternalID: 101, Owner: "acme", Name: "api", DefaultBranch: "main", Topics: []string{"tekitla"}},
			{ExternalID: 102, Owner: "acme", Name: "web", DefaultBranch: "main", Topics: []string{"tekitla"}},
			{ExternalID: 103, Owner: "acme", Name: "sitio", DefaultBranch: "main", Topics: []string{"personal"}},
		},
		openPRs: map[string][]model.RepoEvent{},
	}
}

func (f *fakeGitHub) InstallURL() string {
	return "https://github.com/apps/tekitla-qa/installations/new"
}

func (f *fakeGitHub) Installation(_ context.Context, id int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owner, ok := f.installations[id]
	if !ok {
		return "", domainerrors.NotFoundError("no", nil)
	}
	return owner, nil
}

func (f *fakeGitHub) ListRepos(context.Context, int64) ([]model.RemoteRepo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.repos), nil
}

func (f *fakeGitHub) ListOpenPRs(_ context.Context, _ int64, owner, name string) ([]model.RepoEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.openPRs[owner+"/"+name], nil
}

func (f *fakeGitHub) VerifyWebhook(body []byte, sig string) bool {
	return github.VerifySignature([]byte(testWebhookSecret), body, sig)
}

func (f *fakeGitHub) ParseWebhook(event string, body []byte) (int64, []model.RepoEvent, error) {
	return github.ParseWebhook(event, body)
}

// memIntegrations implementa port.IntegrationRepository en memoria. Process
// trabaja sobre una copia de los links y la guarda solo si fn no falla.
type memIntegrations struct {
	store       *memStore
	mu          sync.Mutex
	connections []*model.Connection
	hashes      map[string][]byte
	repos       []*model.Repository
	links       map[string]*model.IssueLink
	deliveries  map[string]bool
}

func newMemIntegrations(store *memStore) *memIntegrations {
	return &memIntegrations{store: store, hashes: map[string][]byte{}, links: map[string]*model.IssueLink{}, deliveries: map[string]bool{}}
}

var _ port.IntegrationRepository = (*memIntegrations)(nil)

func (m *memIntegrations) countRepos(connID string) int {
	n := 0
	for _, r := range m.repos {
		if r.ConnectionID == connID {
			n++
		}
	}
	return n
}

func (m *memIntegrations) setRepos(c *model.Connection, remote []model.RemoteRepo) {
	keep := map[int64]bool{}
	for _, rr := range remote {
		keep[rr.ExternalID] = true
		found := false
		for _, r := range m.repos {
			if r.ConnectionID == c.ID && r.ExternalID == rr.ExternalID {
				r.Owner, r.Name, r.DefaultBranch = rr.Owner, rr.Name, rr.DefaultBranch
				found = true
			}
		}
		if !found {
			m.repos = append(m.repos, &model.Repository{
				ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", rr.ExternalID), ProjectID: c.ProjectID, ConnectionID: c.ID,
				Provider: c.Provider, Host: c.Host, ExternalID: rr.ExternalID, Owner: rr.Owner, Name: rr.Name,
				DefaultBranch: rr.DefaultBranch, Kind: "code",
			})
		}
	}
	var kept []*model.Repository
	for _, r := range m.repos {
		if r.ConnectionID != c.ID || keep[r.ExternalID] {
			kept = append(kept, r)
			continue
		}
		for id, l := range m.links {
			if l.Repo.ID == r.ID {
				delete(m.links, id)
			}
		}
	}
	m.repos = kept
}

func (m *memIntegrations) connReposLocked(connID string) []*model.Repository {
	var out []*model.Repository
	for _, r := range m.repos {
		if r.ConnectionID == connID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *memIntegrations) CreateConnection(_ context.Context, c *model.Connection, hash []byte, remote []model.RemoteRepo, max int) (*model.Connection, []*model.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, x := range m.connections {
		if x.Provider == c.Provider && x.Host == c.Host && x.Owner == c.Owner {
			return nil, nil, domainerrors.ConflictError("exists", nil).WithReason("CONNECTION_EXISTS")
		}
		if x.ProjectID == c.ProjectID {
			n++
		}
	}
	if n >= max {
		return nil, nil, domainerrors.FailedPreconditionError("limit", nil).WithReason("CONNECTION_LIMIT")
	}
	cp := *c
	m.connections = append(m.connections, &cp)
	m.hashes[c.ID] = hash
	m.setRepos(&cp, remote)
	cp.RepositoryCount = m.countRepos(c.ID)
	return &cp, m.connReposLocked(c.ID), nil
}

func (m *memIntegrations) ListConnections(_ context.Context, projectID string) ([]*model.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*model.Connection
	for _, c := range m.connections {
		if c.ProjectID == projectID {
			cp := *c
			cp.RepositoryCount = m.countRepos(c.ID)
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *memIntegrations) GetConnection(_ context.Context, projectID, id string) (*model.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.connections {
		if c.ProjectID == projectID && c.ID == id {
			cp := *c
			cp.RepositoryCount = m.countRepos(c.ID)
			return &cp, nil
		}
	}
	return nil, domainerrors.NotFoundError("connection not found", nil)
}

func (m *memIntegrations) ReplaceRepositories(_ context.Context, c *model.Connection, remote []model.RemoteRepo) (*model.Connection, []*model.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.connections {
		if x.ID == c.ID && x.ProjectID == c.ProjectID {
			x.Scope, x.Topic, x.Selected = c.Scope, c.Topic, c.Selected
			m.setRepos(x, remote)
			cp := *x
			cp.RepositoryCount = m.countRepos(x.ID)
			return &cp, m.connReposLocked(x.ID), nil
		}
	}
	return nil, nil, domainerrors.NotFoundError("connection not found", nil)
}

func (m *memIntegrations) DeleteConnection(_ context.Context, projectID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, c := range m.connections {
		if c.ProjectID == projectID && c.ID == id {
			m.setRepos(c, nil)
			m.connections = slices.Delete(m.connections, i, i+1)
			return nil
		}
	}
	return domainerrors.NotFoundError("connection not found", nil)
}

func (m *memIntegrations) ListRepositories(_ context.Context, projectID string) ([]*model.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*model.Repository
	for _, r := range m.repos {
		if r.ProjectID == projectID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memIntegrations) ListConnectionRepositories(_ context.Context, connID string) ([]*model.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connReposLocked(connID), nil
}

func (m *memIntegrations) GetRepository(_ context.Context, projectID, id string) (*model.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.repos {
		if r.ProjectID == projectID && r.ID == id {
			return r, nil
		}
	}
	return nil, domainerrors.NotFoundError("repository not found", nil)
}

func (m *memIntegrations) RepositoryByExternalID(_ context.Context, provider model.RepoProvider, host string, ext int64) (*model.Repository, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.repos {
		if r.Provider == provider && r.Host == host && r.ExternalID == ext {
			m.store.mu.Lock()
			key := m.store.projects[r.ProjectID].Key
			m.store.mu.Unlock()
			return r, key, nil
		}
	}
	return nil, "", domainerrors.NotFoundError("repository not found", nil)
}

func (m *memIntegrations) issueOf(id string) *model.Issue {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	for _, is := range m.store.issues {
		if is.ID == id {
			return is
		}
	}
	return nil
}

func cloneLink(l *model.IssueLink) *model.IssueLink {
	cp := *l
	cp.CISuites = map[string]model.CIStatus{}
	for k, v := range l.CISuites {
		cp.CISuites[k] = v
	}
	return &cp
}

func (m *memIntegrations) ListLinks(_ context.Context, projectID string, number int32, limit int) ([]*model.IssueLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*model.IssueLink
	for _, l := range m.links {
		is := m.issueOf(l.IssueID)
		if is == nil || is.ProjectID != projectID || l.Suppressed {
			continue
		}
		if (number > 0 && is.Number != number) || (number == 0 && is.State == model.IssueStateCanceled) {
			continue
		}
		cp := cloneLink(l)
		cp.IssueNumber = is.Number
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IssueNumber != out[j].IssueNumber {
			return out[i].IssueNumber < out[j].IssueNumber
		}
		return out[i].Branch < out[j].Branch
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memIntegrations) Process(_ context.Context, repo *model.Repository, d *port.Delivery, fn func(port.LinkTx) error) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d != nil && m.deliveries[string(d.Provider)+"|"+d.ID] {
		return true, nil
	}
	work := map[string]*model.IssueLink{}
	for id, l := range m.links {
		work[id] = cloneLink(l)
	}
	if err := fn(memLinkTx{m: m, repo: repo, links: work}); err != nil {
		return false, err
	}
	m.links = work
	if d != nil {
		m.deliveries[string(d.Provider)+"|"+d.ID] = true
		for _, c := range m.connections {
			if c.ID == repo.ConnectionID {
				at := d.ReceivedAt
				c.LastDeliveryAt = &at
			}
		}
	}
	return false, nil
}

type memLinkTx struct {
	m     *memIntegrations
	repo  *model.Repository
	links map[string]*model.IssueLink
}

func (t memLinkTx) IssueIDs(_ context.Context, projectID string, numbers []int32) (map[int32]string, error) {
	t.m.store.mu.Lock()
	defer t.m.store.mu.Unlock()
	out := map[int32]string{}
	for _, is := range t.m.store.issues {
		if is.ProjectID == projectID && slices.Contains(numbers, is.Number) {
			out[is.Number] = is.ID
		}
	}
	return out, nil
}

func (t memLinkTx) sorted(keep func(*model.IssueLink) bool) []*model.IssueLink {
	var out []*model.IssueLink
	for _, l := range t.links {
		if l.Repo.ID == t.repo.ID && keep(l) {
			out = append(out, cloneLink(l))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (t memLinkTx) LinksByBranch(_ context.Context, branch string) ([]*model.IssueLink, error) {
	return t.sorted(func(l *model.IssueLink) bool { return l.Branch == branch }), nil
}

func (t memLinkTx) LinksForCommit(_ context.Context, sha, branch string) ([]*model.IssueLink, error) {
	return t.sorted(func(l *model.IssueLink) bool { return l.HeadSHA == sha || (l.HeadSHA == "" && l.Branch == branch) }), nil
}

func (t memLinkTx) CountIssueLinks(_ context.Context, issueID string) (int, error) {
	n := 0
	for _, l := range t.links {
		if l.IssueID == issueID {
			n++
		}
	}
	return n, nil
}

func (t memLinkTx) InsertLink(_ context.Context, l *model.IssueLink) error {
	for _, x := range t.links {
		if x.IssueID == l.IssueID && x.Repo.ID == t.repo.ID && x.Branch == l.Branch {
			return domainerrors.ConflictError("dup link", nil)
		}
	}
	cp := cloneLink(l)
	cp.Repo = t.repo
	t.links[l.ID] = cp
	return nil
}

func (t memLinkTx) UpdateLink(_ context.Context, l *model.IssueLink) error {
	if _, ok := t.links[l.ID]; !ok {
		return domainerrors.NotFoundError("link", nil)
	}
	cp := cloneLink(l)
	cp.Repo = t.repo
	t.links[l.ID] = cp
	return nil
}

func (t memLinkTx) GetLink(_ context.Context, issueID, linkID string) (*model.IssueLink, error) {
	if l, ok := t.links[linkID]; ok && l.IssueID == issueID {
		return cloneLink(l), nil
	}
	return nil, domainerrors.NotFoundError("link not found", nil)
}

// ---- helpers ----

type integrationFixture struct {
	c       clients
	ctx     context.Context
	project *issuesv1.Project
	conn    *issuesv1.Connection
}

// newIntegrationFixture crea el proyecto TK con los issues 1 a 12 y conecta
// la org acme con el topic tekitla (repos api y web).
func newIntegrationFixture(t *testing.T) *integrationFixture {
	t.Helper()
	c := newClients(t)
	ctx := asUser(t, "u1")
	created, err := c.issues.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "TK", Name: "Tekitla"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	p := created.GetProject()
	for i := 1; i <= 12; i++ {
		if _, err := c.issues.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: p.GetId(), Title: fmt.Sprintf("Issue %d", i)}); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
	}
	resp, err := c.repos.ConnectOrganization(ctx, &issuesv1.ConnectOrganizationRequest{
		ProjectId: p.GetId(),
		Provider:  &issuesv1.ConnectOrganizationRequest_Github{Github: &issuesv1.GithubConnection{InstallationId: 77}},
		Scope:     issuesv1.ConnectionScope_CONNECTION_SCOPE_TOPIC,
		Topic:     "tekitla",
	})
	if err != nil {
		t.Fatalf("ConnectOrganization: %v", err)
	}
	return &integrationFixture{c: c, ctx: ctx, project: p, conn: resp.GetConnection()}
}

// deliver manda una entrega firmada (o con la firma que se pase).
func (f *integrationFixture) deliver(t *testing.T, event, delivery, body string, sig ...string) (*issuesv1.ReceiveWebhookResponse, error) {
	t.Helper()
	s := github.Sign([]byte(testWebhookSecret), []byte(body))
	if len(sig) > 0 {
		s = sig[0]
	}
	// Como el gateway: sin x-user-id.
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-request-id", "req-hook")
	return f.c.hooks.ReceiveWebhook(ctx, &issuesv1.ReceiveWebhookRequest{
		Provider: issuesv1.RepoProvider_REPO_PROVIDER_GITHUB, Event: event, DeliveryId: delivery,
		Signature: s, Body: []byte(body),
	})
}

func (f *integrationFixture) mustDeliver(t *testing.T, event, delivery, body string, want issuesv1.WebhookOutcome) {
	t.Helper()
	resp, err := f.deliver(t, event, delivery, body)
	if err != nil {
		t.Fatalf("%s %s: %v", event, delivery, err)
	}
	if resp.GetOutcome() != want {
		t.Fatalf("%s %s: outcome %v, want %v", event, delivery, resp.GetOutcome(), want)
	}
}

func (f *integrationFixture) links(t *testing.T, number int32) []*issuesv1.IssueLink {
	t.Helper()
	resp, err := f.c.repos.ListIssueLinks(f.ctx, &issuesv1.ListIssueLinksRequest{ProjectId: f.project.GetId(), IssueNumber: number})
	if err != nil {
		t.Fatalf("ListIssueLinks: %v", err)
	}
	return resp.GetLinks()
}

func pushBody(repoID int64, branch, sha, msg string) string {
	return fmt.Sprintf(`{"ref":"refs/heads/%s","after":"%s","deleted":false,"repository":{"id":%d},
	  "sender":{"login":"dev-prueba"},"commits":[{"id":"%s"}],"head_commit":{"id":"%s","message":%q}}`,
		branch, sha, repoID, sha, sha, msg)
}

func prBody(repoID int64, number int, branch, sha, state string, merged bool, title, updated string) string {
	return fmt.Sprintf(`{"action":"opened","repository":{"id":%d},"sender":{"login":"dev-prueba"},
	  "pull_request":{"number":%d,"title":%q,"body":"","state":%q,"draft":false,"merged":%t,
	    "updated_at":%q,"requested_reviewers":[],
	    "head":{"ref":%q,"sha":%q,"repo":{"id":%d}},"base":{"repo":{"id":%d}}}}`,
		repoID, number, title, state, merged, updated, branch, sha, repoID, repoID)
}

func checkBody(repoID int64, suite int, branch, sha, status, conclusion string) string {
	return fmt.Sprintf(`{"action":"completed","repository":{"id":%d},"sender":{"login":"github-actions"},
	  "check_suite":{"id":%d,"head_branch":%q,"head_sha":%q,"status":%q,"conclusion":%q}}`,
		repoID, suite, branch, sha, status, conclusion)
}

// ---- tests ----

// CU-016-1: con alcance por topic solo se listan los repos con ese topic.
func TestIntegration_ConnectByTopic(t *testing.T) {
	f := newIntegrationFixture(t)
	repos, err := f.c.repos.ListRepositories(f.ctx, &issuesv1.ListRepositoriesRequest{ProjectId: f.project.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range repos.GetRepositories() {
		names = append(names, r.GetName())
		if r.GetWebUrl() != "https://github.com/acme/"+r.GetName() || r.GetSource() != issuesv1.RepoSource_REPO_SOURCE_ORG {
			t.Errorf("repo: %+v", r)
		}
	}
	if !slices.Equal(names, []string{"api", "web"}) {
		t.Errorf("repos: %v, want [api web]", names)
	}
	if f.conn.GetOwner() != "acme" || f.conn.GetHost() != "github.com" || f.conn.GetRepositoryCount() != 2 {
		t.Errorf("connection: %+v", f.conn)
	}
	list, err := f.c.repos.ListConnections(f.ctx, &issuesv1.ListConnectionsRequest{ProjectId: f.project.GetId()})
	if err != nil || len(list.GetConnections()) != 1 || list.GetGithubInstallUrl() == "" || list.GetGitlabEnabled() {
		t.Errorf("ListConnections: %+v %v", list, err)
	}

	// Cambiar el alcance a selección vuelve a listar.
	upd, err := f.c.repos.UpdateConnection(f.ctx, &issuesv1.UpdateConnectionRequest{
		ProjectId: f.project.GetId(), ConnectionId: f.conn.GetId(),
		Scope: issuesv1.ConnectionScope_CONNECTION_SCOPE_SELECTED, Selected: []string{"sitio"},
	})
	if err != nil || len(upd.GetRepositories()) != 1 || upd.GetRepositories()[0].GetName() != "sitio" {
		t.Errorf("UpdateConnection: %+v %v", upd, err)
	}

	// La misma org otra vez: ALREADY_EXISTS.
	_, err = f.c.repos.ConnectOrganization(f.ctx, &issuesv1.ConnectOrganizationRequest{
		ProjectId: f.project.GetId(),
		Provider:  &issuesv1.ConnectOrganizationRequest_Github{Github: &issuesv1.GithubConnection{InstallationId: 77}},
		Scope:     issuesv1.ConnectionScope_CONNECTION_SCOPE_ALL,
	})
	wantCode(t, "connect twice", err, codes.AlreadyExists)
}

func TestIntegration_ConnectErrors(t *testing.T) {
	f := newIntegrationFixture(t)
	gh := func(id int64) *issuesv1.ConnectOrganizationRequest_Github {
		return &issuesv1.ConnectOrganizationRequest_Github{Github: &issuesv1.GithubConnection{InstallationId: id}}
	}
	// Instalación que la App no tiene.
	_, err := f.c.repos.ConnectOrganization(f.ctx, &issuesv1.ConnectOrganizationRequest{
		ProjectId: f.project.GetId(), Provider: gh(999), Scope: issuesv1.ConnectionScope_CONNECTION_SCOPE_ALL})
	wantCode(t, "unknown installation", err, codes.FailedPrecondition)
	// GitLab todavía no (plan §3.2).
	_, err = f.c.repos.ConnectOrganization(f.ctx, &issuesv1.ConnectOrganizationRequest{
		ProjectId: f.project.GetId(), Scope: issuesv1.ConnectionScope_CONNECTION_SCOPE_ALL,
		Provider: &issuesv1.ConnectOrganizationRequest_Gitlab{Gitlab: &issuesv1.GitlabConnection{BaseUrl: "https://gitlab.example.com"}}})
	wantCode(t, "gitlab", err, codes.FailedPrecondition)
	// Sin alcance.
	_, err = f.c.repos.ConnectOrganization(f.ctx, &issuesv1.ConnectOrganizationRequest{ProjectId: f.project.GetId(), Provider: gh(77)})
	wantCode(t, "no scope", err, codes.InvalidArgument)
	// Proyecto ajeno: NOT_FOUND.
	_, err = f.c.repos.ListConnections(asUser(t, "u2"), &issuesv1.ListConnectionsRequest{ProjectId: f.project.GetId()})
	wantCode(t, "foreign project", err, codes.NotFound)
	_, err = f.c.repos.ListIssueLinks(asUser(t, "u2"), &issuesv1.ListIssueLinksRequest{ProjectId: f.project.GetId()})
	wantCode(t, "foreign links", err, codes.NotFound)
	// Un agente no conecta organizaciones.
	agentCtx := metadata.AppendToOutgoingContext(asUser(t, "u1"), "x-agent-id", "00000000-0000-4000-8000-0000000000ff")
	_, err = f.c.repos.ConnectOrganization(agentCtx, &issuesv1.ConnectOrganizationRequest{
		ProjectId: f.project.GetId(), Provider: gh(77), Scope: issuesv1.ConnectionScope_CONNECTION_SCOPE_ALL})
	wantCode(t, "agent connect", err, codes.PermissionDenied)
	// Sin identidad.
	_, err = f.c.repos.ListRepositories(asUser(t, ""), &issuesv1.ListRepositoriesRequest{ProjectId: f.project.GetId()})
	wantCode(t, "no identity", err, codes.Unauthenticated)
}

// CU-016-2: firma inválida → UNAUTHENTICATED y no se guarda nada.
func TestIntegration_InvalidSignature(t *testing.T) {
	f := newIntegrationFixture(t)
	body := pushBody(101, "feat/tk-9-login", shaA, "feat: login")
	for name, sig := range map[string]string{
		"vacia":        "",
		"otro secreto": github.Sign([]byte("otro-secreto-cualquiera-123"), []byte(body)),
		"de otro body": github.Sign([]byte(testWebhookSecret), []byte(body+" ")),
	} {
		_, err := f.deliver(t, "push", "d-bad-"+name, body, sig)
		wantCode(t, name, err, codes.Unauthenticated)
	}
	// GitLab: ningún secreto vale todavía.
	_, err := f.c.hooks.ReceiveWebhook(context.Background(), &issuesv1.ReceiveWebhookRequest{
		Provider: issuesv1.RepoProvider_REPO_PROVIDER_GITLAB, Event: "Push Hook", DeliveryId: "g1", Token: "tkw_x", Body: []byte(`{}`)})
	wantCode(t, "gitlab", err, codes.Unauthenticated)
	if len(f.c.integr.links) != 0 || len(f.c.integr.deliveries) != 0 {
		t.Errorf("se guardó algo: %d links, %d entregas", len(f.c.integr.links), len(f.c.integr.deliveries))
	}
	// Después, la misma entrega bien firmada se procesa (el id no quedó usado).
	f.mustDeliver(t, "push", "d-bad-vacia", body, issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
}

// CU-016-3, 4 y 9: push liga TK-9; PR y CI rojo; fusionar no mueve el issue.
func TestIntegration_PushPRAndCI(t *testing.T) {
	f := newIntegrationFixture(t)
	f.mustDeliver(t, "push", "d1", pushBody(101, "feat/tk-9-login", shaA, "feat: login con Google\n\ncuerpo"),
		issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	links := f.links(t, 9)
	if len(links) != 1 {
		t.Fatalf("links de TK-9: %v", links)
	}
	l := links[0]
	if l.GetBranch() != "feat/tk-9-login" || l.GetHeadSha() != shaA || l.GetHeadTitle() != "feat: login con Google" ||
		l.GetOrigin() != issuesv1.LinkOrigin_LINK_ORIGIN_BRANCH_NAME || l.GetPrState() != issuesv1.PrState_PR_STATE_NONE ||
		l.GetUpdatedBy() != "dev-prueba" || l.GetRepo().GetName() != "api" ||
		l.GetWebUrl() != "https://github.com/acme/api/tree/feat/tk-9-login" {
		t.Errorf("link tras push: %+v", l)
	}

	// CU-016-4: PR abierto desde esa rama y checks en rojo.
	f.mustDeliver(t, "pull_request", "d2", prBody(101, 7, "feat/tk-9-login", shaA, "open", false, "Login con Google", "2026-11-19T10:00:00Z"),
		issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	f.mustDeliver(t, "check_suite", "d3", checkBody(101, 1, "feat/tk-9-login", shaA, "completed", "success"),
		issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	f.mustDeliver(t, "check_suite", "d4", checkBody(101, 2, "feat/tk-9-login", shaA, "completed", "failure"),
		issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	l = f.links(t, 9)[0]
	if l.GetPrNumber() != 7 || l.GetPrState() != issuesv1.PrState_PR_STATE_OPEN || l.GetCiStatus() != issuesv1.CiStatus_CI_STATUS_FAILED ||
		l.GetWebUrl() != "https://github.com/acme/api/pull/7" {
		t.Errorf("link tras PR y CI: %+v", l)
	}

	// Un push nuevo cambia el head: el CI del commit anterior ya no vale.
	f.mustDeliver(t, "push", "d5", pushBody(101, "feat/tk-9-login", shaB, "fix: tests"), issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	l = f.links(t, 9)[0]
	if l.GetHeadSha() != shaB || l.GetCiStatus() != issuesv1.CiStatus_CI_STATUS_NONE || l.GetPrNumber() != 7 {
		t.Errorf("link tras push nuevo: %+v", l)
	}
	// Un check del commit viejo ya no aplica.
	f.mustDeliver(t, "check_suite", "d6", checkBody(101, 3, "feat/tk-9-login", shaA, "completed", "failure"),
		issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	if got := f.links(t, 9)[0].GetCiStatus(); got != issuesv1.CiStatus_CI_STATUS_NONE {
		t.Errorf("check viejo cambió el CI: %v", got)
	}

	// Un evento más viejo del PR no lo pisa.
	f.mustDeliver(t, "pull_request", "d7", prBody(101, 7, "feat/tk-9-login", shaB, "closed", false, "viejo", "2026-11-18T10:00:00Z"),
		issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	if got := f.links(t, 9)[0]; got.GetPrState() != issuesv1.PrState_PR_STATE_OPEN || got.GetPrTitle() != "Login con Google" {
		t.Errorf("evento viejo pisó el PR: %+v", got)
	}

	// CU-016-9: se fusiona el PR y el issue no cambia de estado.
	before, err := f.c.issues.GetIssue(f.ctx, &issuesv1.GetIssueRequest{ProjectId: f.project.GetId(), Number: 9})
	if err != nil {
		t.Fatal(err)
	}
	f.mustDeliver(t, "pull_request", "d8", prBody(101, 7, "feat/tk-9-login", shaB, "closed", true, "Login con Google", "2026-11-19T12:00:00Z"),
		issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	if got := f.links(t, 9)[0].GetPrState(); got != issuesv1.PrState_PR_STATE_MERGED {
		t.Errorf("PR fusionado: %v", got)
	}
	after, err := f.c.issues.GetIssue(f.ctx, &issuesv1.GetIssueRequest{ProjectId: f.project.GetId(), Number: 9})
	if err != nil {
		t.Fatal(err)
	}
	if after.GetIssue().GetStatusId() != before.GetIssue().GetStatusId() || after.GetIssue().GetState() != before.GetIssue().GetState() {
		t.Errorf("CU-016-9: el issue cambió de estado: %v → %v", before.GetIssue().GetState(), after.GetIssue().GetState())
	}

	// Borrar la rama la marca borrada.
	f.mustDeliver(t, "push", "d9", `{"ref":"refs/heads/feat/tk-9-login","after":"0000000000000000000000000000000000000000","deleted":true,"repository":{"id":101},"sender":{"login":"dev-prueba"}}`,
		issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	if !f.links(t, 9)[0].GetBranchDeleted() {
		t.Error("rama borrada no marcada")
	}
}

// CU-016-5: la misma entrega dos veces deja el mismo resultado.
func TestIntegration_DuplicateDelivery(t *testing.T) {
	f := newIntegrationFixture(t)
	body := pushBody(101, "feat/tk-9-login", shaA, "feat: login")
	f.mustDeliver(t, "push", "d-dup", body, issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	first := f.links(t, 0)
	f.mustDeliver(t, "push", "d-dup", body, issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_DUPLICATE)
	second := f.links(t, 0)
	if len(first) != 1 || len(second) != 1 || !proto.Equal(first[0], second[0]) {
		t.Errorf("CU-016-5: distinto tras repetir:\n%v\n%v", first, second)
	}
}

// CU-016-6: una rama sin clave conocida no se liga ni falla.
func TestIntegration_UnknownBranchAndRepo(t *testing.T) {
	f := newIntegrationFixture(t)
	f.mustDeliver(t, "push", "d1", pushBody(101, "fix/typo", shaA, "fix"), issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	// Un número que no existe en el proyecto, ni de otro proyecto.
	f.mustDeliver(t, "push", "d2", pushBody(101, "feat/tk-999-x", shaA, "x"), issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	f.mustDeliver(t, "push", "d3", pushBody(101, "feat/rnd-9-x", shaA, "x"), issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	if got := f.links(t, 0); len(got) != 0 {
		t.Errorf("CU-016-6: links: %v", got)
	}
	// Repo no conectado (sitio no tiene el topic) y evento que no interesa.
	f.mustDeliver(t, "push", "d4", pushBody(103, "feat/tk-9-x", shaA, "x"), issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_IGNORED)
	f.mustDeliver(t, "ping", "d5", `{"zen":"hola"}`, issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_IGNORED)
	// Sin id de entrega: INVALID_ARGUMENT (después de la firma).
	_, err := f.deliver(t, "push", "", pushBody(101, "feat/tk-9-x", shaA, "x"))
	wantCode(t, "no delivery id", err, codes.InvalidArgument)
}

// Ligado por el texto del PR, por issue-<n> y manual; quitar lo suprime.
func TestIntegration_LinkByTextManualAndUnlink(t *testing.T) {
	f := newIntegrationFixture(t)
	// PR desde una rama sin clave que menciona TK-12 en el título.
	f.mustDeliver(t, "pull_request", "d1", prBody(102, 3, "mejora-login", shaA, "open", false, "Mejora del login (TK-12)", "2026-11-19T10:00:00Z"),
		issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	l := f.links(t, 12)
	if len(l) != 1 || l[0].GetOrigin() != issuesv1.LinkOrigin_LINK_ORIGIN_PR_TEXT || l[0].GetRepo().GetName() != "web" {
		t.Fatalf("link por texto: %v", l)
	}
	// issue-<n> en la rama.
	f.mustDeliver(t, "push", "d2", pushBody(101, "feat/issue-4-algo", shaB, "x"), issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	if got := f.links(t, 4); len(got) != 1 {
		t.Errorf("issue-4: %v", got)
	}

	// Quitar: suprimido, y un push nuevo de la rama no lo recrea.
	_, err := f.c.repos.UnlinkIssue(f.ctx, &issuesv1.UnlinkIssueRequest{ProjectId: f.project.GetId(), Number: 4, LinkId: f.links(t, 4)[0].GetId()})
	if err != nil {
		t.Fatal(err)
	}
	f.mustDeliver(t, "push", "d3", pushBody(101, "feat/issue-4-algo", shaA, "y"), issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	if got := f.links(t, 4); len(got) != 0 {
		t.Errorf("link suprimido volvió: %v", got)
	}

	// Ligar a mano lo vuelve a mostrar, con el estado que ya tenía.
	repos, _ := f.c.repos.ListRepositories(f.ctx, &issuesv1.ListRepositoriesRequest{ProjectId: f.project.GetId()})
	apiID := repos.GetRepositories()[0].GetId()
	linked, err := f.c.repos.LinkIssue(f.ctx, &issuesv1.LinkIssueRequest{ProjectId: f.project.GetId(), Number: 4, RepositoryId: apiID, Branch: "feat/issue-4-algo"})
	if err != nil || linked.GetLink().GetHeadSha() == "" {
		t.Fatalf("LinkIssue: %+v %v", linked, err)
	}
	// Ligar a mano otra rama a TK-1: nuevo link manual.
	manual, err := f.c.repos.LinkIssue(f.ctx, &issuesv1.LinkIssueRequest{ProjectId: f.project.GetId(), Number: 1, RepositoryId: apiID, Branch: "spike/algo"})
	if err != nil || manual.GetLink().GetOrigin() != issuesv1.LinkOrigin_LINK_ORIGIN_MANUAL {
		t.Fatalf("LinkIssue manual: %+v %v", manual, err)
	}
	_, err = f.c.repos.LinkIssue(f.ctx, &issuesv1.LinkIssueRequest{ProjectId: f.project.GetId(), Number: 99, RepositoryId: apiID, Branch: "x"})
	wantCode(t, "issue inexistente", err, codes.NotFound)
	_, err = f.c.repos.LinkIssue(f.ctx, &issuesv1.LinkIssueRequest{ProjectId: f.project.GetId(), Number: 1, RepositoryId: apiID, Branch: ""})
	wantCode(t, "rama vacía", err, codes.InvalidArgument)
}

// Sync liga los PR abiertos y desconectar borra repos y links.
func TestIntegration_SyncAndDisconnect(t *testing.T) {
	f := newIntegrationFixture(t)
	f.c.gh.openPRs["acme/api"] = []model.RepoEvent{{
		Kind: model.RepoEventPR, Actor: "dev-prueba", Branch: "feat/tk-2-algo",
		PR: &model.PREvent{Number: 5, Title: "Algo", State: model.PRStateOpen, HeadSHA: shaA},
	}}
	res, err := f.c.repos.SyncRepositories(f.ctx, &issuesv1.SyncRepositoriesRequest{ProjectId: f.project.GetId(), ConnectionId: f.conn.GetId()})
	if err != nil || res.GetLinksUpdated() != 1 {
		t.Fatalf("Sync: %+v %v", res, err)
	}
	if l := f.links(t, 2); len(l) != 1 || l[0].GetPrNumber() != 5 {
		t.Errorf("link de sync: %v", l)
	}
	if _, err := f.c.repos.DisconnectOrganization(f.ctx, &issuesv1.DisconnectOrganizationRequest{ProjectId: f.project.GetId(), ConnectionId: f.conn.GetId()}); err != nil {
		t.Fatal(err)
	}
	if l := f.links(t, 0); len(l) != 0 {
		t.Errorf("links tras desconectar: %v", l)
	}
	_, err = f.c.repos.DisconnectOrganization(f.ctx, &issuesv1.DisconnectOrganizationRequest{ProjectId: f.project.GetId(), ConnectionId: f.conn.GetId()})
	wantCode(t, "disconnect twice", err, codes.NotFound)
}

// CU-016-8: ninguna respuesta de la API de repos y conexiones lleva llaves,
// tokens ni secretos.
func TestIntegration_NoSecretsInResponses(t *testing.T) {
	f := newIntegrationFixture(t)
	f.mustDeliver(t, "push", "d1", pushBody(101, "feat/tk-9-login", shaA, "feat"), issuesv1.WebhookOutcome_WEBHOOK_OUTCOME_PROCESSED)
	pid := f.project.GetId()
	var msgs []proto.Message
	add := func(m proto.Message, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, m)
	}
	add(f.c.repos.ListConnections(f.ctx, &issuesv1.ListConnectionsRequest{ProjectId: pid}))
	add(f.c.repos.ListRepositories(f.ctx, &issuesv1.ListRepositoriesRequest{ProjectId: pid}))
	add(f.c.repos.ListIssueLinks(f.ctx, &issuesv1.ListIssueLinksRequest{ProjectId: pid}))
	add(f.c.repos.UpdateConnection(f.ctx, &issuesv1.UpdateConnectionRequest{ProjectId: pid, ConnectionId: f.conn.GetId(),
		Scope: issuesv1.ConnectionScope_CONNECTION_SCOPE_ALL}))
	add(f.c.repos.SyncRepositories(f.ctx, &issuesv1.SyncRepositoriesRequest{ProjectId: pid, ConnectionId: f.conn.GetId()}))
	add(f.c.repos.ConnectOrganization(asUser(t, "u1"), &issuesv1.ConnectOrganizationRequest{
		ProjectId: mustProject(t, f.c, "OT"),
		Provider:  &issuesv1.ConnectOrganizationRequest_Github{Github: &issuesv1.GithubConnection{InstallationId: 78}},
		Scope:     issuesv1.ConnectionScope_CONNECTION_SCOPE_ALL,
	}))
	for _, m := range msgs {
		js := protojson.Format(m)
		for _, bad := range []string{testWebhookSecret, "BEGIN", "PRIVATE", "ghs_", "glpat-", "tkw_", "secret", "token_hash", "installation_id", "installationId"} {
			if strings.Contains(js, bad) {
				t.Errorf("CU-016-8: %T contiene %q:\n%s", m, bad, js)
			}
		}
	}
}

func mustProject(t *testing.T, c clients, key string) string {
	t.Helper()
	c.gh.installations[78] = "acme-otra"
	p, err := c.issues.CreateProject(asUser(t, "u1"), &issuesv1.CreateProjectRequest{Key: key, Name: key})
	if err != nil {
		t.Fatal(err)
	}
	return p.GetProject().GetId()
}

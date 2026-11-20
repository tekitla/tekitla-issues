package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// IntegrationService (issue 016): validaciones y propagación de errores del
// repo y de la App. El recorrido completo (webhooks, ligado, sync) está en
// handler/integration_handler_test.go y contra Postgres en make test-pg.

const intProjectID = "00000000-0000-4000-8000-0000000000a1"

// mockIntegrationRepo satisface port.IntegrationRepository con funciones
// opcionales; las que no se setean fallan si se llaman.
type mockIntegrationRepo struct {
	port.IntegrationRepository
	createFn func(c *model.Connection, repos []model.RemoteRepo) (*model.Connection, []*model.Repository, error)
	getFn    func(projectID, id string) (*model.Connection, error)
	byExtFn  func(ext int64) (*model.Repository, string, error)
}

func (m *mockIntegrationRepo) CreateConnection(_ context.Context, c *model.Connection, _ []byte, repos []model.RemoteRepo, _ int) (*model.Connection, []*model.Repository, error) {
	return m.createFn(c, repos)
}

func (m *mockIntegrationRepo) GetConnection(_ context.Context, projectID, id string) (*model.Connection, error) {
	return m.getFn(projectID, id)
}

func (m *mockIntegrationRepo) RepositoryByExternalID(_ context.Context, _ model.RepoProvider, _ string, ext int64) (*model.Repository, string, error) {
	return m.byExtFn(ext)
}

// mockGitHubApp satisface port.GitHubApp.
type mockGitHubApp struct {
	port.GitHubApp
	installationFn func(id int64) (string, error)
	listReposFn    func() ([]model.RemoteRepo, error)
	verifyOK       bool
	parseFn        func(event string) (int64, []model.RepoEvent, error)
}

func (m *mockGitHubApp) InstallURL() string { return "https://github.com/apps/x/installations/new" }
func (m *mockGitHubApp) Installation(_ context.Context, id int64) (string, error) {
	return m.installationFn(id)
}
func (m *mockGitHubApp) ListRepos(context.Context, int64) ([]model.RemoteRepo, error) {
	return m.listReposFn()
}
func (m *mockGitHubApp) VerifyWebhook([]byte, string) bool { return m.verifyOK }
func (m *mockGitHubApp) ParseWebhook(event string, _ []byte) (int64, []model.RepoEvent, error) {
	return m.parseFn(event)
}

func intSvc(repo *mockIntegrationRepo, gh port.GitHubApp) *application.IntegrationService {
	ws := &mockWorkspaceRepo{getPersonalFn: func(context.Context, string) (*model.Workspace, error) {
		return &model.Workspace{ID: "w1", OwnerUID: "u1"}, nil
	}}
	projects := &mockProjectRepo{getByIDFn: func(_ context.Context, _ []string, id string) (*model.Project, error) {
		if id != intProjectID {
			return nil, domainerrors.NotFoundError("project not found", nil)
		}
		return &model.Project{ID: id, WorkspaceID: "w1", Key: "TK"}, nil
	}}
	return application.NewIntegrationService(ws, projects, repo, gh)
}

func wantReason(t *testing.T, what string, err error, reason string) {
	t.Helper()
	var de *domainerrors.DomainError
	if !errors.As(err, &de) || de.Reason != reason {
		t.Errorf("%s: got %v, want reason %s", what, err, reason)
	}
}

func TestConnectOrganization_Validations(t *testing.T) {
	ctx := context.Background()
	gh := &mockGitHubApp{
		installationFn: func(id int64) (string, error) {
			if id != 77 {
				return "", domainerrors.NotFoundError("no", nil)
			}
			return "acme", nil
		},
		listReposFn: func() ([]model.RemoteRepo, error) {
			return []model.RemoteRepo{{ExternalID: 1, Owner: "acme", Name: "api", Topics: []string{"tekitla"}}, {ExternalID: 2, Owner: "acme", Name: "web"}}, nil
		},
	}
	var saved []model.RemoteRepo
	repo := &mockIntegrationRepo{createFn: func(c *model.Connection, repos []model.RemoteRepo) (*model.Connection, []*model.Repository, error) {
		saved = repos
		return c, nil, nil
	}}
	svc := intSvc(repo, gh)
	in := application.ConnectInput{ProjectID: intProjectID, Provider: model.RepoProviderGitHub, InstallationID: 77,
		Scope: model.ConnectionScopeTopic, Topic: "Tekitla"}

	conn, _, err := svc.ConnectOrganization(ctx, "u1", in)
	if err != nil || conn.Owner != "acme" || conn.Topic != "tekitla" || len(saved) != 1 || saved[0].Name != "api" {
		t.Fatalf("connect: %+v %v %v", conn, saved, err)
	}

	bad := in
	bad.InstallationID = 78
	_, _, err = svc.ConnectOrganization(ctx, "u1", bad)
	wantReason(t, "instalación desconocida", err, "GITHUB_INSTALLATION_NOT_FOUND")

	bad = in
	bad.Provider = model.RepoProviderGitLab
	_, _, err = svc.ConnectOrganization(ctx, "u1", bad)
	wantReason(t, "gitlab", err, "GITLAB_NOT_ENABLED")

	bad = in
	bad.InstallationID = 0
	if _, _, err = svc.ConnectOrganization(ctx, "u1", bad); !domainerrors.IsValidation(err) {
		t.Errorf("sin installation_id: %v", err)
	}
	bad = in
	bad.Scope = model.ConnectionScopeUnspecified
	if _, _, err = svc.ConnectOrganization(ctx, "u1", bad); !domainerrors.IsValidation(err) {
		t.Errorf("sin alcance: %v", err)
	}
	bad = in
	bad.ProjectID = "00000000-0000-4000-8000-0000000000ff"
	if _, _, err = svc.ConnectOrganization(ctx, "u1", bad); !domainerrors.IsNotFound(err) {
		t.Errorf("proyecto ajeno: %v", err)
	}
	if _, _, err = svc.ConnectOrganization(ctx, "", in); !domainerrors.IsUnauthorized(err) {
		t.Errorf("sin usuario: %v", err)
	}

	// Sin App en el ambiente.
	_, _, err = intSvc(repo, nil).ConnectOrganization(ctx, "u1", in)
	wantReason(t, "sin App", err, "GITHUB_NOT_ENABLED")

	// Errores de la App y del repo se propagan.
	gh.listReposFn = func() ([]model.RemoteRepo, error) {
		return nil, domainerrors.ServiceUnavailableError("GitHub caído", nil)
	}
	if _, _, err = svc.ConnectOrganization(ctx, "u1", in); err == nil || domainerrors.IsNotFound(err) {
		t.Errorf("GitHub caído: %v", err)
	}
	gh.listReposFn = func() ([]model.RemoteRepo, error) { return nil, nil }
	repo.createFn = func(*model.Connection, []model.RemoteRepo) (*model.Connection, []*model.Repository, error) {
		return nil, nil, domainerrors.ConflictError("x", nil).WithReason("CONNECTION_EXISTS")
	}
	_, _, err = svc.ConnectOrganization(ctx, "u1", in)
	wantReason(t, "ya conectada", err, "CONNECTION_EXISTS")
}

// Con la App desinstalada, sincronizar no responde NOT_FOUND (que diría que
// la conexión no existe) sino FAILED_PRECONDITION.
func TestSyncRepositories_InstallationGone(t *testing.T) {
	gh := &mockGitHubApp{listReposFn: func() ([]model.RemoteRepo, error) {
		return nil, domainerrors.NotFoundError("not found in GitHub", nil)
	}}
	repo := &mockIntegrationRepo{getFn: func(projectID, id string) (*model.Connection, error) {
		return &model.Connection{ID: id, ProjectID: projectID, Provider: model.RepoProviderGitHub, InstallationID: 77}, nil
	}}
	_, err := intSvc(repo, gh).SyncRepositories(context.Background(), "u1", intProjectID, "00000000-0000-4000-8000-0000000000c1")
	wantReason(t, "desinstalada", err, "GITHUB_INSTALLATION_NOT_FOUND")
}

func TestReceiveWebhook_Validations(t *testing.T) {
	ctx := context.Background()
	gh := &mockGitHubApp{parseFn: func(string) (int64, []model.RepoEvent, error) { return 0, nil, nil }}
	repo := &mockIntegrationRepo{}
	svc := intSvc(repo, gh)
	in := application.WebhookInput{Provider: model.RepoProviderGitHub, Event: "push", DeliveryID: "d1", Signature: "x", Body: []byte(`{}`)}

	// Firma inválida: UNAUTHENTICATED antes de mirar nada más.
	if _, err := svc.ReceiveWebhook(ctx, in); !domainerrors.IsUnauthorized(err) {
		t.Errorf("firma inválida: %v", err)
	}
	gh.verifyOK = true
	noID := in
	noID.DeliveryID = ""
	if _, err := svc.ReceiveWebhook(ctx, noID); !domainerrors.IsValidation(err) {
		t.Errorf("sin id de entrega: %v", err)
	}
	if out, err := svc.ReceiveWebhook(ctx, in); err != nil || out != application.WebhookIgnored {
		t.Errorf("evento sin repo: %v %v", out, err)
	}
	// Repo no conectado: ignorado; error de la base: se propaga.
	gh.parseFn = func(string) (int64, []model.RepoEvent, error) {
		return 42, []model.RepoEvent{{Kind: model.RepoEventPush, Branch: "x"}}, nil
	}
	repo.byExtFn = func(int64) (*model.Repository, string, error) {
		return nil, "", domainerrors.NotFoundError("repository not found", nil)
	}
	if out, err := svc.ReceiveWebhook(ctx, in); err != nil || out != application.WebhookIgnored {
		t.Errorf("repo no conectado: %v %v", out, err)
	}
	repo.byExtFn = func(int64) (*model.Repository, string, error) {
		return nil, "", domainerrors.InternalError("db", nil)
	}
	if _, err := svc.ReceiveWebhook(ctx, in); !domainerrors.IsInternal(err) {
		t.Errorf("error de la base: %v", err)
	}
	// GitLab: ningún secreto vale todavía.
	gl := in
	gl.Provider = model.RepoProviderGitLab
	if _, err := svc.ReceiveWebhook(ctx, gl); !domainerrors.IsUnauthorized(err) {
		t.Errorf("gitlab: %v", err)
	}
}

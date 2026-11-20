package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/infra/postgres"
	"github.com/tekitla/tekitla-issues/app/integrations/github"
)

// Test del repo de la integración (issue 016) contra un Postgres de verdad.
// Se salta sin TEKITLA_PG_TEST_URL, que tiene que apuntar a una base vacía
// y descartable (aplica todas las migraciones): scripts/test_pg.sh levanta
// una en docker y la borra al terminar. Nunca contra QA ni prod.

const pgSecret = "secreto-webhook-de-prueba-0123456789"

type pgFakeGitHub struct{ repos []model.RemoteRepo }

func (f pgFakeGitHub) InstallURL() string { return "https://github.com/apps/x/installations/new" }
func (f pgFakeGitHub) Installation(context.Context, int64) (string, error) {
	return "acme", nil
}
func (f pgFakeGitHub) ListRepos(context.Context, int64) ([]model.RemoteRepo, error) {
	return f.repos, nil
}
func (f pgFakeGitHub) ListOpenPRs(context.Context, int64, string, string) ([]model.RepoEvent, error) {
	return nil, nil
}
func (f pgFakeGitHub) PullRequest(context.Context, int64, string, string, int) (model.RepoEvent, error) {
	return model.RepoEvent{}, domainerrors.NotFoundError("no", nil)
}
func (f pgFakeGitHub) VerifyWebhook(body []byte, sig string) bool {
	return github.VerifySignature([]byte(pgSecret), body, sig)
}
func (f pgFakeGitHub) ParseWebhook(event string, body []byte) (int64, []model.RepoEvent, error) {
	return github.ParseWebhook(event, body)
}

func TestIntegrationRepository_Postgres(t *testing.T) {
	url := os.Getenv("TEKITLA_PG_TEST_URL")
	if url == "" {
		t.Skip("sin TEKITLA_PG_TEST_URL (ver scripts/test_pg.sh)")
	}
	db, err := postgres.NewDB(url, false)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("../../../migrations/*.up.sql")
	sort.Strings(files)
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if err := db.Exec(string(b)).Error; err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	ctx := context.Background()
	workspaces, projects := postgres.NewWorkspaceRepository(db), postgres.NewProjectRepository(db)
	issueSvc := application.NewIssueService(workspaces, projects, postgres.NewIssueRepository(db),
		postgres.NewStatusRepository(db), postgres.NewLabelRepository(db), postgres.NewActorRepository(db))
	project, err := application.NewProjectService(workspaces, projects).CreateProject(ctx, "u1", application.CreateProjectInput{Key: "TK", Name: "Tekitla"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := issueSvc.CreateIssue(ctx, model.Author{UserID: "u1"}, application.CreateIssueInput{ProjectID: project.ID, Title: "Issue"}); err != nil {
			t.Fatal(err)
		}
	}
	repo := postgres.NewIntegrationRepository(db)
	gh := pgFakeGitHub{repos: []model.RemoteRepo{
		{ExternalID: 101, Owner: "acme", Name: "api", DefaultBranch: "main", Topics: []string{"tekitla"}},
		{ExternalID: 102, Owner: "acme", Name: "web", DefaultBranch: "main"},
	}}
	svc := application.NewIntegrationService(workspaces, projects, repo, gh)

	conn, repos, err := svc.ConnectOrganization(ctx, "u1", application.ConnectInput{
		ProjectID: project.ID, Provider: model.RepoProviderGitHub, InstallationID: 77,
		Scope: model.ConnectionScopeTopic, Topic: "tekitla"})
	if err != nil || len(repos) != 1 || repos[0].Name != "api" || conn.RepositoryCount != 1 || conn.Topic != "tekitla" {
		t.Fatalf("Connect: %+v %+v %v", conn, repos, err)
	}
	if _, _, err := svc.ConnectOrganization(ctx, "u1", application.ConnectInput{
		ProjectID: project.ID, Provider: model.RepoProviderGitHub, InstallationID: 77, Scope: model.ConnectionScopeAll}); !domainerrors.IsConflict(err) {
		t.Errorf("connect twice: %v", err)
	}

	deliver := func(event, id, body string) application.WebhookOutcome {
		t.Helper()
		out, err := svc.ReceiveWebhook(ctx, application.WebhookInput{Provider: model.RepoProviderGitHub, Event: event,
			DeliveryID: id, Signature: github.Sign([]byte(pgSecret), []byte(body)), Body: []byte(body)})
		if err != nil {
			t.Fatalf("%s %s: %v", event, id, err)
		}
		return out
	}
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	push := `{"ref":"refs/heads/feat/tk-9-tk-3","after":"` + sha + `","repository":{"id":101},"sender":{"login":"dev-prueba"},
	  "commits":[{"id":"` + sha + `"}],"head_commit":{"id":"` + sha + `","message":"feat: ñandú\n\nx"}}`
	if got := deliver("push", "d1", push); got != application.WebhookProcessed {
		t.Fatalf("push: %v", got)
	}
	if got := deliver("push", "d1", push); got != application.WebhookDuplicate {
		t.Errorf("push repetido: %v", got)
	}
	check := `{"repository":{"id":101},"check_suite":{"id":9,"head_branch":"feat/tk-9-tk-3","head_sha":"` + sha + `","status":"completed","conclusion":"failure"}}`
	deliver("check_suite", "d2", check)
	pr := `{"action":"opened","repository":{"id":101},"sender":{"login":"dev-prueba"},"pull_request":{"number":4,"title":"TK-9",
	  "state":"open","merged":false,"updated_at":"2026-11-19T10:00:00Z","requested_reviewers":[{"login":"r"}],
	  "head":{"ref":"feat/tk-9-tk-3","sha":"` + sha + `","repo":{"id":101}},"base":{"repo":{"id":101}}}}`
	deliver("pull_request", "d3", pr)

	links, err := svc.ListIssueLinks(ctx, "u1", project.ID, 0)
	if err != nil || len(links) != 2 {
		t.Fatalf("links: %+v %v", links, err)
	}
	for _, l := range links {
		if l.HeadSHA != sha || l.HeadTitle != "feat: ñandú" || l.CIStatus != model.CIStatusFailed || l.PRNumber != 4 ||
			l.PRState != model.PRStateOpen || !l.ReviewRequested || l.CISuites["9"] != model.CIStatusFailed ||
			l.Repo.Host != "github.com" || l.Repo.Name != "api" || l.UpdatedBy != "dev-prueba" {
			t.Errorf("link: %+v", l)
		}
	}
	if links[0].IssueNumber != 3 || links[1].IssueNumber != 9 {
		t.Errorf("orden por issue: %d %d", links[0].IssueNumber, links[1].IssueNumber)
	}

	// Entregas concurrentes de la misma rama: un solo link por issue (lock
	// por repo) y cada entrega una vez.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := `{"ref":"refs/heads/feat/tk-5-x","after":"` + sha + `","repository":{"id":101},"commits":[]}`
			_, _ = svc.ReceiveWebhook(ctx, application.WebhookInput{Provider: model.RepoProviderGitHub, Event: "push",
				DeliveryID: "c" + string(rune('0'+i%3)), Signature: github.Sign([]byte(pgSecret), []byte(b)), Body: []byte(b)})
		}(i)
	}
	wg.Wait()
	if l, _ := svc.ListIssueLinks(ctx, "u1", project.ID, 5); len(l) != 1 {
		t.Errorf("concurrencia: %d links de TK-5", len(l))
	}

	// Quitar, ligar a mano y desconectar.
	if err := svc.UnlinkIssue(ctx, "u1", project.ID, 3, links[0].ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := svc.ListIssueLinks(ctx, "u1", project.ID, 3); len(l) != 0 {
		t.Errorf("suprimido visible: %v", l)
	}
	if l, err := svc.LinkIssue(ctx, "u1", project.ID, 3, repos[0].ID, "feat/tk-9-tk-3"); err != nil || l.Suppressed {
		t.Errorf("LinkIssue: %+v %v", l, err)
	}
	if _, rs, err := svc.UpdateConnection(ctx, "u1", application.UpdateConnectionInput{ProjectID: project.ID, ConnectionID: conn.ID,
		Scope: model.ConnectionScopeAll}); err != nil || len(rs) != 2 {
		t.Errorf("UpdateConnection: %v %v", rs, err)
	}
	if conns, err := svc.ListConnections(ctx, "u1", project.ID); err != nil || conns.Connections[0].LastDeliveryAt == nil {
		t.Errorf("last_delivery_at: %+v %v", conns, err)
	}
	if err := svc.DisconnectOrganization(ctx, "u1", project.ID, conn.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := svc.ListIssueLinks(ctx, "u1", project.ID, 0); len(l) != 0 {
		t.Errorf("links tras desconectar: %d", len(l))
	}
}

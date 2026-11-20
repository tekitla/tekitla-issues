package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// IntegrationRepository implementa port.IntegrationRepository sobre Postgres
// (issue 016). El hash del secreto de webhook de GitLab nunca se lee: solo se
// escribe y se compara en un WHERE.
type IntegrationRepository struct{ db *gorm.DB }

var _ port.IntegrationRepository = (*IntegrationRepository)(nil)

// NewIntegrationRepository construye el repo.
func NewIntegrationRepository(db *gorm.DB) *IntegrationRepository {
	return &IntegrationRepository{db: db}
}

type connectionRow struct {
	ID              string
	ProjectID       string
	Provider        string
	Host            string
	Owner           string
	InstallationID  *int64
	CredentialRef   *string
	SyncMode        *string
	PollIntervalMin *int
	Scope           string
	Topic           *string
	Selected        string
	CreatedByUID    string
	CreatedAt       time.Time
	LastDeliveryAt  *time.Time
	RepositoryCount int
}

// connectionColumns nunca incluye webhook_secret_hash.
const connectionColumns = `c.id, c.project_id, c.provider, c.host, c.owner, c.installation_id, c.credential_ref,
	c.sync_mode, c.poll_interval_min, c.scope, c.topic, array_to_string(c.selected, ',') AS selected,
	c.created_by_uid, c.created_at, c.last_delivery_at,
	(SELECT count(*) FROM issues.repositories r WHERE r.connection_id = c.id) AS repository_count`

func (r connectionRow) toModel() *model.Connection {
	c := &model.Connection{
		ID: r.ID, ProjectID: r.ProjectID, Provider: model.RepoProvider(r.Provider), Host: r.Host, Owner: r.Owner,
		Scope: model.ConnectionScope(r.Scope), CreatedByUID: r.CreatedByUID, CreatedAt: r.CreatedAt.UTC(),
		LastDeliveryAt: utcPtr(r.LastDeliveryAt), RepositoryCount: r.RepositoryCount,
	}
	if r.InstallationID != nil {
		c.InstallationID = *r.InstallationID
	}
	if r.CredentialRef != nil {
		c.CredentialRef = *r.CredentialRef
	}
	if r.SyncMode != nil {
		c.SyncMode = model.GitLabSyncMode(*r.SyncMode)
	}
	if r.PollIntervalMin != nil {
		c.PollIntervalMin = *r.PollIntervalMin
	}
	if r.Topic != nil {
		c.Topic = *r.Topic
	}
	if r.Selected != "" {
		c.Selected = strings.Split(r.Selected, ",")
	}
	return c
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

type repositoryRow struct {
	ID            string
	ProjectID     string
	ConnectionID  string
	Provider      string
	Host          string
	ExternalID    int64
	Owner         string
	Name          string
	DefaultBranch string
	Kind          string
}

const repositoryColumns = `r.id, r.project_id, r.connection_id, c.provider, c.host, r.external_id, r.owner, r.name,
	r.default_branch, r.kind`

func (r repositoryRow) toModel() *model.Repository {
	return &model.Repository{
		ID: r.ID, ProjectID: r.ProjectID, ConnectionID: r.ConnectionID, Provider: model.RepoProvider(r.Provider),
		Host: r.Host, ExternalID: r.ExternalID, Owner: r.Owner, Name: r.Name, DefaultBranch: r.DefaultBranch, Kind: r.Kind,
	}
}

func nullable[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// CreateConnection guarda la conexión y sus repos con el lock del proyecto.
func (r *IntegrationRepository) CreateConnection(ctx context.Context, c *model.Connection, webhookSecretHash []byte, repos []model.RemoteRepo, maxConnections int) (*model.Connection, []*model.Repository, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockProject(tx, c.ProjectID); err != nil {
			return err
		}
		var count int64
		if err := tx.Raw(`SELECT count(*) FROM issues.repo_connections WHERE project_id = ?`, c.ProjectID).Scan(&count).Error; err != nil {
			return err
		}
		if count >= int64(maxConnections) {
			return domainerrors.FailedPreconditionError("project has the maximum number of connections",
				map[string]any{"max": maxConnections}).WithReason("CONNECTION_LIMIT")
		}
		err := tx.Exec(
			`INSERT INTO issues.repo_connections (id, project_id, provider, host, owner, installation_id, credential_ref,
			     webhook_secret_hash, sync_mode, poll_interval_min, scope, topic, selected, created_by_uid, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, string_to_array(?, ','), ?, ?)`,
			c.ID, c.ProjectID, string(c.Provider), c.Host, c.Owner, nullable(c.InstallationID), nullable(c.CredentialRef),
			webhookSecretHash, nullable(string(c.SyncMode)), nullable(c.PollIntervalMin), string(c.Scope), nullable(c.Topic),
			strings.Join(c.Selected, ","), c.CreatedByUID, c.CreatedAt,
		).Error
		if err != nil {
			return err
		}
		return syncRepos(tx, c, repos)
	})
	if err != nil {
		return nil, nil, connectionError(err, c)
	}
	return r.reload(ctx, c.ProjectID, c.ID)
}

func connectionError(err error, c *model.Connection) error {
	var de *domainerrors.DomainError
	if errors.As(err, &de) {
		return de
	}
	te := translateError(err, "connection", "save", map[string]any{"project_id": c.ProjectID})
	if domainerrors.IsConflict(te) {
		return domainerrors.ConflictError("that organization or group is already connected", nil).WithReason("CONNECTION_EXISTS")
	}
	return te
}

// syncRepos deja como repos de la conexión exactamente repos.
func syncRepos(tx *gorm.DB, c *model.Connection, repos []model.RemoteRepo) error {
	keep := make([]int64, 0, len(repos))
	for _, rr := range repos {
		keep = append(keep, rr.ExternalID)
		err := tx.Exec(
			`INSERT INTO issues.repositories (id, connection_id, project_id, external_id, owner, name, default_branch)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (connection_id, external_id)
			 DO UPDATE SET owner = EXCLUDED.owner, name = EXCLUDED.name, default_branch = EXCLUDED.default_branch`,
			uuid.NewString(), c.ID, c.ProjectID, rr.ExternalID, rr.Owner, rr.Name, rr.DefaultBranch,
		).Error
		if err != nil {
			return err
		}
	}
	if len(keep) == 0 {
		return tx.Exec(`DELETE FROM issues.repositories WHERE connection_id = ?`, c.ID).Error
	}
	return tx.Exec(`DELETE FROM issues.repositories WHERE connection_id = ? AND external_id NOT IN ?`, c.ID, keep).Error
}

func (r *IntegrationRepository) reload(ctx context.Context, projectID, id string) (*model.Connection, []*model.Repository, error) {
	conn, err := r.GetConnection(ctx, projectID, id)
	if err != nil {
		return nil, nil, err
	}
	repos, err := r.ListConnectionRepositories(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return conn, repos, nil
}

// ListConnections devuelve las conexiones del proyecto, las más viejas primero.
func (r *IntegrationRepository) ListConnections(ctx context.Context, projectID string) ([]*model.Connection, error) {
	var rows []connectionRow
	err := r.db.WithContext(ctx).Raw(`SELECT `+connectionColumns+` FROM issues.repo_connections c
	  WHERE c.project_id = ? ORDER BY c.created_at, c.id`, projectID).Scan(&rows).Error
	if err != nil {
		return nil, translateError(err, "connection", "list", map[string]any{"project_id": projectID})
	}
	out := make([]*model.Connection, len(rows))
	for i, row := range rows {
		out[i] = row.toModel()
	}
	return out, nil
}

// GetConnection devuelve una conexión del proyecto.
func (r *IntegrationRepository) GetConnection(ctx context.Context, projectID, id string) (*model.Connection, error) {
	var rows []connectionRow
	err := r.db.WithContext(ctx).Raw(`SELECT `+connectionColumns+` FROM issues.repo_connections c
	  WHERE c.project_id = ? AND c.id = ?`, projectID, id).Scan(&rows).Error
	if err != nil {
		return nil, translateError(err, "connection", "get", map[string]any{"id": id})
	}
	if len(rows) == 0 {
		return nil, domainerrors.NotFoundError("connection not found", map[string]any{"id": id})
	}
	return rows[0].toModel(), nil
}

// ReplaceRepositories guarda el alcance y deja exactamente esos repos.
func (r *IntegrationRepository) ReplaceRepositories(ctx context.Context, c *model.Connection, repos []model.RemoteRepo) (*model.Connection, []*model.Repository, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`UPDATE issues.repo_connections SET scope = ?, topic = ?, selected = string_to_array(?, ',')
		  WHERE id = ? AND project_id = ?`,
			string(c.Scope), nullable(c.Topic), strings.Join(c.Selected, ","), c.ID, c.ProjectID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return domainerrors.NotFoundError("connection not found", map[string]any{"id": c.ID})
		}
		return syncRepos(tx, c, repos)
	})
	if err != nil {
		return nil, nil, connectionError(err, c)
	}
	return r.reload(ctx, c.ProjectID, c.ID)
}

// DeleteConnection borra la conexión (sus repos y links caen por CASCADE).
func (r *IntegrationRepository) DeleteConnection(ctx context.Context, projectID, id string) error {
	res := r.db.WithContext(ctx).Exec(`DELETE FROM issues.repo_connections WHERE project_id = ? AND id = ?`, projectID, id)
	if res.Error != nil {
		return translateError(res.Error, "connection", "delete", map[string]any{"id": id})
	}
	if res.RowsAffected == 0 {
		return domainerrors.NotFoundError("connection not found", map[string]any{"id": id})
	}
	return nil
}

func (r *IntegrationRepository) listRepos(ctx context.Context, where string, args ...any) ([]*model.Repository, error) {
	var rows []repositoryRow
	err := r.db.WithContext(ctx).Raw(`SELECT `+repositoryColumns+` FROM issues.repositories r
	  JOIN issues.repo_connections c ON c.id = r.connection_id WHERE `+where+
		` ORDER BY lower(r.owner), lower(r.name), r.id`, args...).Scan(&rows).Error
	if err != nil {
		return nil, translateError(err, "repository", "list", nil)
	}
	out := make([]*model.Repository, len(rows))
	for i, row := range rows {
		out[i] = row.toModel()
	}
	return out, nil
}

// ListRepositories devuelve los repos del proyecto.
func (r *IntegrationRepository) ListRepositories(ctx context.Context, projectID string) ([]*model.Repository, error) {
	return r.listRepos(ctx, `r.project_id = ?`, projectID)
}

// ListConnectionRepositories devuelve los repos de una conexión.
func (r *IntegrationRepository) ListConnectionRepositories(ctx context.Context, connectionID string) ([]*model.Repository, error) {
	return r.listRepos(ctx, `r.connection_id = ?`, connectionID)
}

// GetRepository devuelve un repo del proyecto.
func (r *IntegrationRepository) GetRepository(ctx context.Context, projectID, id string) (*model.Repository, error) {
	repos, err := r.listRepos(ctx, `r.project_id = ? AND r.id = ?`, projectID, id)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, domainerrors.NotFoundError("repository not found", map[string]any{"id": id})
	}
	return repos[0], nil
}

// RepositoryByExternalID busca el repo conectado y la clave de su proyecto.
func (r *IntegrationRepository) RepositoryByExternalID(ctx context.Context, provider model.RepoProvider, host string, externalID int64) (*model.Repository, string, error) {
	// Sin struct embebido: GORM no lo llena en un Raw (bug visto en el 009).
	var rows []struct {
		ID            string
		ProjectID     string
		ConnectionID  string
		Provider      string
		Host          string
		ExternalID    int64
		Owner         string
		Name          string
		DefaultBranch string
		Kind          string
		ProjectKey    string
	}
	err := r.db.WithContext(ctx).Raw(`SELECT `+repositoryColumns+`, p.key AS project_key FROM issues.repositories r
	  JOIN issues.repo_connections c ON c.id = r.connection_id
	  JOIN issues.projects p ON p.id = r.project_id
	  WHERE c.provider = ? AND c.host = ? AND r.external_id = ?
	  ORDER BY r.id LIMIT 1`, string(provider), host, externalID).Scan(&rows).Error
	if err != nil {
		return nil, "", translateError(err, "repository", "get", nil)
	}
	if len(rows) == 0 {
		return nil, "", domainerrors.NotFoundError("repository not found", nil)
	}
	row := rows[0]
	repo := repositoryRow{ID: row.ID, ProjectID: row.ProjectID, ConnectionID: row.ConnectionID, Provider: row.Provider,
		Host: row.Host, ExternalID: row.ExternalID, Owner: row.Owner, Name: row.Name, DefaultBranch: row.DefaultBranch,
		Kind: row.Kind}
	return repo.toModel(), row.ProjectKey, nil
}

type linkRow struct {
	ID              string
	IssueID         string
	IssueNumber     int32
	RepositoryID    string
	Branch          string
	HeadSHA         *string
	HeadTitle       string
	Commits         int
	PRNumber        *int
	PRTitle         string
	PRState         string
	PRUpdatedAt     *time.Time
	ReviewRequested bool
	CIStatus        string
	CISuites        string
	Origin          string
	Suppressed      bool
	BranchDeleted   bool
	UpdatedBy       string
	UpdatedAt       time.Time
	// Repo.
	RepoProjectID    string
	RepoConnectionID string
	RepoProvider     string
	RepoHost         string
	RepoExternalID   int64
	RepoOwner        string
	RepoName         string
	RepoDefault      string
	RepoKind         string
}

const linkColumns = `l.id, l.issue_id, i.number AS issue_number, l.repository_id, l.branch, l.head_sha, l.head_title,
	l.commits, l.pr_number, l.pr_title, l.pr_state, l.pr_updated_at, l.review_requested, l.ci_status,
	l.ci_suites::text AS ci_suites, l.origin, l.suppressed, l.branch_deleted, l.updated_by, l.updated_at,
	r.project_id AS repo_project_id, r.connection_id AS repo_connection_id, c.provider AS repo_provider,
	c.host AS repo_host, r.external_id AS repo_external_id, r.owner AS repo_owner, r.name AS repo_name,
	r.default_branch AS repo_default, r.kind AS repo_kind`

const linkFrom = ` FROM issues.issue_links l
	JOIN issues.issues i ON i.id = l.issue_id
	JOIN issues.repositories r ON r.id = l.repository_id
	JOIN issues.repo_connections c ON c.id = r.connection_id`

func (row linkRow) toModel() *model.IssueLink {
	l := &model.IssueLink{
		ID: row.ID, IssueID: row.IssueID, IssueNumber: row.IssueNumber, Branch: row.Branch, HeadTitle: row.HeadTitle,
		Commits: row.Commits, PRTitle: row.PRTitle, PRState: model.PRState(row.PRState), PRUpdatedAt: utcPtr(row.PRUpdatedAt),
		ReviewRequested: row.ReviewRequested, CIStatus: model.CIStatus(row.CIStatus), Origin: model.LinkOrigin(row.Origin),
		Suppressed: row.Suppressed, BranchDeleted: row.BranchDeleted, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt.UTC(),
		CISuites: map[string]model.CIStatus{},
		Repo: &model.Repository{
			ID: row.RepositoryID, ProjectID: row.RepoProjectID, ConnectionID: row.RepoConnectionID,
			Provider: model.RepoProvider(row.RepoProvider), Host: row.RepoHost, ExternalID: row.RepoExternalID,
			Owner: row.RepoOwner, Name: row.RepoName, DefaultBranch: row.RepoDefault, Kind: row.RepoKind,
		},
	}
	if row.HeadSHA != nil {
		l.HeadSHA = *row.HeadSHA
	}
	if row.PRNumber != nil {
		l.PRNumber = *row.PRNumber
	}
	if row.CISuites != "" {
		_ = json.Unmarshal([]byte(row.CISuites), &l.CISuites)
	}
	return l
}

func scanLinks(db *gorm.DB, where string, args ...any) ([]*model.IssueLink, error) {
	var rows []linkRow
	if err := db.Raw(`SELECT `+linkColumns+linkFrom+` WHERE `+where, args...).Scan(&rows).Error; err != nil {
		return nil, translateError(err, "issue link", "list", nil)
	}
	out := make([]*model.IssueLink, len(rows))
	for i, row := range rows {
		out[i] = row.toModel()
	}
	return out, nil
}

// ListLinks devuelve los links sin suprimir de un issue o del proyecto.
func (r *IntegrationRepository) ListLinks(ctx context.Context, projectID string, issueNumber int32, limit int) ([]*model.IssueLink, error) {
	db := r.db.WithContext(ctx)
	if issueNumber > 0 {
		return scanLinks(db, `i.project_id = ? AND i.number = ? AND NOT l.suppressed
		  ORDER BY l.updated_at DESC, l.id LIMIT ?`, projectID, issueNumber, limit)
	}
	// Los limit más recientes (por updated_at) y, entre ellos, por issue.
	return scanLinks(db, `l.id IN (
	    SELECT l2.id FROM issues.issue_links l2 JOIN issues.issues i2 ON i2.id = l2.issue_id
	     WHERE i2.project_id = ? AND i2.state <> 'canceled' AND NOT l2.suppressed
	     ORDER BY l2.updated_at DESC, l2.id LIMIT ?)
	  ORDER BY i.number, l.updated_at DESC, l.id`, projectID, limit)
}

// Process corre fn en una transacción con el lock del repo.
func (r *IntegrationRepository) Process(ctx context.Context, repo *model.Repository, d *port.Delivery, fn func(port.LinkTx) error) (bool, error) {
	duplicate := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Un lock por repo: dos entregas de la misma rama no crean el mismo
		// link a la vez.
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('issue_links:' || ?))`, repo.ID).Error; err != nil {
			return err
		}
		if d != nil {
			res := tx.Exec(`INSERT INTO issues.webhook_deliveries (provider, delivery_id, event, received_at)
			  VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, string(d.Provider), d.ID, d.Event, d.ReceivedAt)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				duplicate = true
				return nil
			}
			if err := tx.Exec(`DELETE FROM issues.webhook_deliveries WHERE received_at < ?`,
				d.ReceivedAt.Add(-model.DeliveryRetention)).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE issues.repo_connections SET last_delivery_at = ? WHERE id = ?`,
				d.ReceivedAt, repo.ConnectionID).Error; err != nil {
				return err
			}
		}
		return fn(linkTx{tx: tx.WithContext(ctx), repo: repo})
	})
	if err != nil {
		var de *domainerrors.DomainError
		if errors.As(err, &de) {
			return false, de
		}
		return false, translateError(err, "issue link", "save", map[string]any{"repository_id": repo.ID})
	}
	return duplicate, nil
}

// linkTx implementa port.LinkTx dentro de Process.
type linkTx struct {
	tx   *gorm.DB
	repo *model.Repository
}

func (t linkTx) IssueIDs(_ context.Context, projectID string, numbers []int32) (map[int32]string, error) {
	var rows []struct {
		ID     string
		Number int32
	}
	if err := t.tx.Raw(`SELECT id, number FROM issues.issues WHERE project_id = ? AND number IN ?`, projectID, numbers).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[int32]string, len(rows))
	for _, row := range rows {
		out[row.Number] = row.ID
	}
	return out, nil
}

func (t linkTx) LinksByBranch(_ context.Context, branch string) ([]*model.IssueLink, error) {
	return scanLinks(t.tx, `l.repository_id = ? AND l.branch = ? ORDER BY l.id`, t.repo.ID, branch)
}

func (t linkTx) LinksForCommit(_ context.Context, sha, branch string) ([]*model.IssueLink, error) {
	return scanLinks(t.tx, `l.repository_id = ? AND (l.head_sha = ? OR (l.head_sha IS NULL AND l.branch = ?)) ORDER BY l.id`,
		t.repo.ID, sha, branch)
}

func (t linkTx) CountIssueLinks(_ context.Context, issueID string) (int, error) {
	var n int64
	err := t.tx.Raw(`SELECT count(*) FROM issues.issue_links WHERE issue_id = ?`, issueID).Scan(&n).Error
	return int(n), err
}

func (t linkTx) GetLink(_ context.Context, issueID, linkID string) (*model.IssueLink, error) {
	links, err := scanLinks(t.tx, `l.issue_id = ? AND l.id = ?`, issueID, linkID)
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, domainerrors.NotFoundError("link not found", nil)
	}
	return links[0], nil
}

func linkArgs(l *model.IssueLink) ([]any, error) {
	suites, err := json.Marshal(l.CISuites)
	if err != nil {
		return nil, err
	}
	if l.CISuites == nil {
		suites = []byte("{}")
	}
	return []any{
		nullable(l.HeadSHA), l.HeadTitle, l.Commits, nullable(l.PRNumber), l.PRTitle, string(l.PRState), l.PRUpdatedAt,
		l.ReviewRequested, string(l.CIStatus), string(suites), string(l.Origin), l.Suppressed, l.BranchDeleted,
		l.UpdatedBy, l.UpdatedAt,
	}, nil
}

func (t linkTx) InsertLink(_ context.Context, l *model.IssueLink) error {
	args, err := linkArgs(l)
	if err != nil {
		return err
	}
	return t.tx.Exec(`INSERT INTO issues.issue_links (head_sha, head_title, commits, pr_number, pr_title, pr_state,
	    pr_updated_at, review_requested, ci_status, ci_suites, origin, suppressed, branch_deleted, updated_by, updated_at,
	    id, issue_id, repository_id, branch)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		append(args, l.ID, l.IssueID, t.repo.ID, l.Branch)...).Error
}

func (t linkTx) UpdateLink(_ context.Context, l *model.IssueLink) error {
	args, err := linkArgs(l)
	if err != nil {
		return err
	}
	return t.tx.Exec(`UPDATE issues.issue_links SET head_sha = ?, head_title = ?, commits = ?, pr_number = ?, pr_title = ?,
	    pr_state = ?, pr_updated_at = ?, review_requested = ?, ci_status = ?, ci_suites = ?::jsonb, origin = ?,
	    suppressed = ?, branch_deleted = ?, updated_by = ?, updated_at = ?
	  WHERE id = ? AND repository_id = ?`,
		append(args, l.ID, t.repo.ID)...).Error
}

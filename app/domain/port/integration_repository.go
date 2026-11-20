package port

import (
	"context"
	"time"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// IntegrationRepository define la persistencia de la integración con GitHub
// y GitLab (issue 016): conexiones, repos, links y entregas de webhooks.
// Nunca guarda secretos en claro: del secreto de webhook de GitLab, solo el
// SHA-256, que no sale del repo.
//
// La pertenencia del proyecto a los espacios del usuario la verifica el
// service antes de llamar a los métodos con projectID.
//
// Contrato de errores: no existe (o es de otro proyecto) -> NotFoundError;
// organización o grupo ya conectado -> ConflictError (reason
// CONNECTION_EXISTS); topes -> FailedPreconditionError; resto ->
// InternalError.
type IntegrationRepository interface {
	// CreateConnection guarda la conexión (con el hash del secreto del
	// webhook, si es GitLab con webhook) y sus repos, con el lock del
	// proyecto: si ya tiene maxConnections, FailedPrecondition (reason
	// CONNECTION_LIMIT).
	CreateConnection(ctx context.Context, c *model.Connection, webhookSecretHash []byte, repos []model.RemoteRepo, maxConnections int) (*model.Connection, []*model.Repository, error)
	// ListConnections devuelve las conexiones del proyecto, las más viejas
	// primero, con su cantidad de repos.
	ListConnections(ctx context.Context, projectID string) ([]*model.Connection, error)
	// GetConnection devuelve una conexión del proyecto.
	GetConnection(ctx context.Context, projectID, id string) (*model.Connection, error)
	// ReplaceRepositories cambia el alcance de la conexión y deja como sus
	// repos exactamente repos: actualiza los que siguen (por external_id),
	// agrega los nuevos y borra los que salieron (con sus links).
	ReplaceRepositories(ctx context.Context, c *model.Connection, repos []model.RemoteRepo) (*model.Connection, []*model.Repository, error)
	// DeleteConnection borra la conexión del proyecto, sus repos y sus links.
	DeleteConnection(ctx context.Context, projectID, id string) error
	// ListRepositories devuelve los repos del proyecto por owner y nombre.
	ListRepositories(ctx context.Context, projectID string) ([]*model.Repository, error)
	// ListConnectionRepositories devuelve los repos de una conexión.
	ListConnectionRepositories(ctx context.Context, connectionID string) ([]*model.Repository, error)
	// GetRepository devuelve un repo del proyecto.
	GetRepository(ctx context.Context, projectID, id string) (*model.Repository, error)
	// RepositoryByExternalID busca el repo conectado de ese proveedor y host
	// con ese id del proveedor, y la clave de su proyecto. NotFound si no
	// está conectado.
	RepositoryByExternalID(ctx context.Context, provider model.RepoProvider, host string, externalID int64) (*model.Repository, string, error)
	// ListLinks devuelve los links sin suprimir de un issue (issueNumber > 0)
	// o del proyecto (0, sin los de issues descartados), por número de issue
	// y los más recientes primero, como mucho limit.
	ListLinks(ctx context.Context, projectID string, issueNumber int32, limit int) ([]*model.IssueLink, error)
	// Process corre fn en una transacción con el lock del repo. Con d, antes
	// registra la entrega: si ya estaba, no corre fn y devuelve duplicate.
	// Si fn falla, no queda nada (tampoco la entrega). Actualiza
	// last_delivery_at de la conexión del repo y borra las entregas de más de
	// model.DeliveryRetention.
	Process(ctx context.Context, repo *model.Repository, d *Delivery, fn func(LinkTx) error) (duplicate bool, err error)
}

// Delivery es una entrega de webhook, para descartar las repetidas.
type Delivery struct {
	Provider   model.RepoProvider
	ID         string
	Event      string
	ReceivedAt time.Time
}

// LinkTx son las operaciones sobre links dentro de Process. Todas son del
// repo de Process.
type LinkTx interface {
	// IssueIDs devuelve el id de los issues del proyecto con esos números
	// (los que no existen no están en el mapa).
	IssueIDs(ctx context.Context, projectID string, numbers []int32) (map[int32]string, error)
	// LinksByBranch devuelve los links de una rama (también suprimidos).
	LinksByBranch(ctx context.Context, branch string) ([]*model.IssueLink, error)
	// LinksForCommit devuelve los links cuyo head es sha, y los de la rama
	// branch que todavía no tienen head (también suprimidos).
	LinksForCommit(ctx context.Context, sha, branch string) ([]*model.IssueLink, error)
	// CountIssueLinks cuenta los links de un issue (también suprimidos).
	CountIssueLinks(ctx context.Context, issueID string) (int, error)
	// InsertLink crea un link.
	InsertLink(ctx context.Context, l *model.IssueLink) error
	// UpdateLink guarda un link existente (por id).
	UpdateLink(ctx context.Context, l *model.IssueLink) error
	// GetLink devuelve un link del issue.
	GetLink(ctx context.Context, issueID, linkID string) (*model.IssueLink, error)
}

// GitHubApp es la GitHub App de este ambiente (issue 016): la API (con la
// llave privada, que no sale del adapter) y los webhooks (con su secreto).
type GitHubApp interface {
	// InstallURL es la página para instalar la App.
	InstallURL() string
	// Installation devuelve la cuenta (org) de una instalación de la App.
	// NotFound si la App no tiene esa instalación.
	Installation(ctx context.Context, installationID int64) (owner string, err error)
	// ListRepos devuelve los repos que ve la instalación.
	ListRepos(ctx context.Context, installationID int64) ([]model.RemoteRepo, error)
	// ListOpenPRs devuelve los PR abiertos de un repo como eventos.
	ListOpenPRs(ctx context.Context, installationID int64, owner, name string) ([]model.RepoEvent, error)
	// PullRequest devuelve un PR (en cualquier estado) como evento. NotFound
	// si no existe.
	PullRequest(ctx context.Context, installationID int64, owner, name string, number int) (model.RepoEvent, error)
	// VerifyWebhook comprueba la firma X-Hub-Signature-256 del cuerpo, en
	// tiempo constante.
	VerifyWebhook(body []byte, signature string) bool
	// ParseWebhook traduce una entrega a eventos del repo. repoID 0: la
	// entrega no es de un repo o el evento no interesa.
	ParseWebhook(event string, body []byte) (repoID int64, events []model.RepoEvent, err error)
}

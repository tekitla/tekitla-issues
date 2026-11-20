package mapper

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Mapeo de la integración con GitHub y GitLab (issue 016). Los mensajes se
// arman campo por campo: ninguno lleva secretos (el hash del secreto de
// webhook ni siquiera llega al modelo).

// ProviderToProto traduce el proveedor.
func ProviderToProto(p model.RepoProvider) issuesv1.RepoProvider {
	switch p {
	case model.RepoProviderGitHub:
		return issuesv1.RepoProvider_REPO_PROVIDER_GITHUB
	case model.RepoProviderGitLab:
		return issuesv1.RepoProvider_REPO_PROVIDER_GITLAB
	}
	return issuesv1.RepoProvider_REPO_PROVIDER_UNSPECIFIED
}

// ProviderFromProto traduce el proveedor del request.
func ProviderFromProto(p issuesv1.RepoProvider) model.RepoProvider {
	switch p {
	case issuesv1.RepoProvider_REPO_PROVIDER_GITHUB:
		return model.RepoProviderGitHub
	case issuesv1.RepoProvider_REPO_PROVIDER_GITLAB:
		return model.RepoProviderGitLab
	}
	return model.RepoProviderUnspecified
}

// ScopeFromProto traduce el alcance de una conexión.
func ScopeFromProto(s issuesv1.ConnectionScope) model.ConnectionScope {
	switch s {
	case issuesv1.ConnectionScope_CONNECTION_SCOPE_ALL:
		return model.ConnectionScopeAll
	case issuesv1.ConnectionScope_CONNECTION_SCOPE_TOPIC:
		return model.ConnectionScopeTopic
	case issuesv1.ConnectionScope_CONNECTION_SCOPE_SELECTED:
		return model.ConnectionScopeSelected
	}
	return model.ConnectionScopeUnspecified
}

func scopeToProto(s model.ConnectionScope) issuesv1.ConnectionScope {
	switch s {
	case model.ConnectionScopeAll:
		return issuesv1.ConnectionScope_CONNECTION_SCOPE_ALL
	case model.ConnectionScopeTopic:
		return issuesv1.ConnectionScope_CONNECTION_SCOPE_TOPIC
	case model.ConnectionScopeSelected:
		return issuesv1.ConnectionScope_CONNECTION_SCOPE_SELECTED
	}
	return issuesv1.ConnectionScope_CONNECTION_SCOPE_UNSPECIFIED
}

func syncModeToProto(m model.GitLabSyncMode) issuesv1.GitlabSyncMode {
	switch m {
	case model.GitLabSyncModeWebhook:
		return issuesv1.GitlabSyncMode_GITLAB_SYNC_MODE_WEBHOOK
	case model.GitLabSyncModePoll:
		return issuesv1.GitlabSyncMode_GITLAB_SYNC_MODE_POLL
	}
	return issuesv1.GitlabSyncMode_GITLAB_SYNC_MODE_UNSPECIFIED
}

// ConnectionToProto traduce una conexión (sin secretos).
func ConnectionToProto(c *model.Connection) *issuesv1.Connection {
	if c == nil {
		return nil
	}
	out := &issuesv1.Connection{
		Id:                  c.ID,
		ProjectId:           c.ProjectID,
		Provider:            ProviderToProto(c.Provider),
		Host:                c.Host,
		Owner:               c.Owner,
		Scope:               scopeToProto(c.Scope),
		Topic:               c.Topic,
		Selected:            c.Selected,
		CredentialRef:       c.CredentialRef,
		SyncMode:            syncModeToProto(c.SyncMode),
		PollIntervalMinutes: int32(c.PollIntervalMin),
		CreatedAt:           timestamppb.New(c.CreatedAt),
		RepositoryCount:     int32(c.RepositoryCount),
	}
	if c.LastDeliveryAt != nil {
		out.LastDeliveryAt = timestamppb.New(*c.LastDeliveryAt)
	}
	return out
}

// ConnectionsToProto traduce una lista de conexiones.
func ConnectionsToProto(cs []*model.Connection) []*issuesv1.Connection {
	out := make([]*issuesv1.Connection, len(cs))
	for i, c := range cs {
		out[i] = ConnectionToProto(c)
	}
	return out
}

// RepositoryToProto traduce un repo.
func RepositoryToProto(r *model.Repository) *issuesv1.Repository {
	if r == nil {
		return nil
	}
	source := issuesv1.RepoSource_REPO_SOURCE_ORG
	if r.Source() == "group" {
		source = issuesv1.RepoSource_REPO_SOURCE_GROUP
	}
	kind := issuesv1.RepoKind_REPO_KIND_CODE
	if r.Kind == "docs" {
		kind = issuesv1.RepoKind_REPO_KIND_DOCS
	}
	return &issuesv1.Repository{
		Id:            r.ID,
		ProjectId:     r.ProjectID,
		ConnectionId:  r.ConnectionID,
		Provider:      ProviderToProto(r.Provider),
		Owner:         r.Owner,
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		Source:        source,
		Kind:          kind,
		WebUrl:        r.WebURL(),
	}
}

// RepositoriesToProto traduce una lista de repos.
func RepositoriesToProto(rs []*model.Repository) []*issuesv1.Repository {
	out := make([]*issuesv1.Repository, len(rs))
	for i, r := range rs {
		out[i] = RepositoryToProto(r)
	}
	return out
}

func prStateToProto(s model.PRState) issuesv1.PrState {
	switch s {
	case model.PRStateNone:
		return issuesv1.PrState_PR_STATE_NONE
	case model.PRStateDraft:
		return issuesv1.PrState_PR_STATE_DRAFT
	case model.PRStateOpen:
		return issuesv1.PrState_PR_STATE_OPEN
	case model.PRStateMerged:
		return issuesv1.PrState_PR_STATE_MERGED
	case model.PRStateClosed:
		return issuesv1.PrState_PR_STATE_CLOSED
	}
	return issuesv1.PrState_PR_STATE_UNSPECIFIED
}

func ciToProto(s model.CIStatus) issuesv1.CiStatus {
	switch s {
	case model.CIStatusNone:
		return issuesv1.CiStatus_CI_STATUS_NONE
	case model.CIStatusPending:
		return issuesv1.CiStatus_CI_STATUS_PENDING
	case model.CIStatusSuccess:
		return issuesv1.CiStatus_CI_STATUS_SUCCESS
	case model.CIStatusFailed:
		return issuesv1.CiStatus_CI_STATUS_FAILED
	case model.CIStatusCanceled:
		return issuesv1.CiStatus_CI_STATUS_CANCELED
	}
	return issuesv1.CiStatus_CI_STATUS_UNSPECIFIED
}

func originToProto(o model.LinkOrigin) issuesv1.LinkOrigin {
	switch o {
	case model.LinkOriginBranchName:
		return issuesv1.LinkOrigin_LINK_ORIGIN_BRANCH_NAME
	case model.LinkOriginPRText:
		return issuesv1.LinkOrigin_LINK_ORIGIN_PR_TEXT
	case model.LinkOriginManual:
		return issuesv1.LinkOrigin_LINK_ORIGIN_MANUAL
	}
	return issuesv1.LinkOrigin_LINK_ORIGIN_UNSPECIFIED
}

// IssueLinkToProto traduce un link. El repo va reducido a lo que se muestra.
func IssueLinkToProto(l *model.IssueLink) *issuesv1.IssueLink {
	if l == nil {
		return nil
	}
	out := &issuesv1.IssueLink{
		Id:              l.ID,
		IssueNumber:     l.IssueNumber,
		Branch:          l.Branch,
		HeadSha:         l.HeadSHA,
		HeadTitle:       l.HeadTitle,
		Commits:         int32(l.Commits),
		PrNumber:        int32(l.PRNumber),
		PrTitle:         l.PRTitle,
		PrState:         prStateToProto(l.PRState),
		ReviewRequested: l.ReviewRequested,
		CiStatus:        ciToProto(l.CIStatus),
		Origin:          originToProto(l.Origin),
		UpdatedBy:       l.UpdatedBy,
		UpdatedAt:       timestamppb.New(l.UpdatedAt),
		BranchDeleted:   l.BranchDeleted,
		WebUrl:          l.WebURL(),
	}
	if l.Repo != nil {
		out.Repo = &issuesv1.Repository{
			Id:       l.Repo.ID,
			Provider: ProviderToProto(l.Repo.Provider),
			Owner:    l.Repo.Owner,
			Name:     l.Repo.Name,
			WebUrl:   l.Repo.WebURL(),
		}
	}
	return out
}

// IssueLinksToProto traduce una lista de links.
func IssueLinksToProto(ls []*model.IssueLink) []*issuesv1.IssueLink {
	out := make([]*issuesv1.IssueLink, len(ls))
	for i, l := range ls {
		out[i] = IssueLinkToProto(l)
	}
	return out
}

package github

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/tekitla/tekitla-issues/app/domain/model"
)

// Traducción de las entregas de GitHub a model.RepoEvent. Los payloads son
// contenido ajeno (OWASP API10): se leen con structs mínimos, se recortan los
// textos y se descarta lo que no tiene forma (sha, número, rama).

type payloadRepo struct {
	ID int64 `json:"id"`
}

type payloadUser struct {
	Login string `json:"login"`
}

type apiPR struct {
	Number             int           `json:"number"`
	Title              string        `json:"title"`
	Body               string        `json:"body"`
	State              string        `json:"state"`
	Draft              bool          `json:"draft"`
	Merged             bool          `json:"merged"`
	MergedAt           *time.Time    `json:"merged_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
	User               payloadUser   `json:"user"`
	RequestedReviewers []payloadUser `json:"requested_reviewers"`
	RequestedTeams     []struct {
		ID int64 `json:"id"`
	} `json:"requested_teams"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			ID    int64       `json:"id"`
			Owner payloadUser `json:"owner"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Repo payloadRepo `json:"repo"`
	} `json:"base"`
}

func (p *apiPR) state() model.PRState {
	switch {
	case p.Merged || p.MergedAt != nil:
		return model.PRStateMerged
	case p.State == "closed":
		return model.PRStateClosed
	case p.Draft:
		return model.PRStateDraft
	default:
		return model.PRStateOpen
	}
}

// branch es la rama de origen del PR. La de un fork lleva el dueño del fork
// adelante («dueño:rama») para no mezclarla con una rama del repo con el
// mismo nombre.
func (p *apiPR) branch() string {
	if p.Head.Repo.ID != 0 && p.Base.Repo.ID != 0 && p.Head.Repo.ID != p.Base.Repo.ID {
		return p.Head.Repo.Owner.Login + ":" + p.Head.Ref
	}
	return p.Head.Ref
}

// event traduce el PR a un evento. ok=false si no tiene forma.
func (p *apiPR) event(actor string) (model.RepoEvent, bool) {
	branch := p.branch()
	if p.Number <= 0 || !model.ValidBranch(branch) {
		return model.RepoEvent{}, false
	}
	head := p.Head.SHA
	if !model.IsSHA(head) {
		head = ""
	}
	return model.RepoEvent{
		Kind:   model.RepoEventPR,
		Actor:  actor,
		Branch: branch,
		PR: &model.PREvent{
			Number:          p.Number,
			Title:           strings.TrimSpace(p.Title),
			Body:            model.Truncate(p.Body, 20000),
			State:           p.state(),
			HeadSHA:         head,
			ReviewRequested: len(p.RequestedReviewers)+len(p.RequestedTeams) > 0,
			UpdatedAt:       p.UpdatedAt.UTC(),
		},
	}, true
}

// ParseWebhook traduce una entrega de GitHub. Eventos que interesan: push,
// pull_request, pull_request_review y check_suite. El resto (ping,
// installation…) devuelve repoID 0 y se ignora.
func (a *App) ParseWebhook(event string, body []byte) (int64, []model.RepoEvent, error) {
	return ParseWebhook(event, body)
}

// ParseWebhook es App.ParseWebhook sin la App (tests).
func ParseWebhook(event string, body []byte) (int64, []model.RepoEvent, error) {
	switch event {
	case "push":
		return parsePush(body)
	case "pull_request", "pull_request_review":
		return parsePR(event, body)
	case "check_suite":
		return parseCheckSuite(body)
	default:
		return 0, nil, nil
	}
}

func parsePush(body []byte) (int64, []model.RepoEvent, error) {
	var p struct {
		Ref        string      `json:"ref"`
		After      string      `json:"after"`
		Deleted    bool        `json:"deleted"`
		Repository payloadRepo `json:"repository"`
		Sender     payloadUser `json:"sender"`
		Commits    []struct {
			ID string `json:"id"`
		} `json:"commits"`
		HeadCommit *struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		} `json:"head_commit"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return 0, nil, errBadPayload("push")
	}
	branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
	if !ok || p.Repository.ID <= 0 || !model.ValidBranch(branch) {
		// Tags y refs raras: no interesan.
		return 0, nil, nil
	}
	ev := model.RepoEvent{Kind: model.RepoEventPush, Actor: p.Sender.Login, Branch: branch, Deleted: p.Deleted}
	if !p.Deleted {
		if !model.IsSHA(p.After) {
			return 0, nil, errBadPayload("push.after")
		}
		ev.HeadSHA = p.After
		ev.Commits = len(p.Commits)
		if p.HeadCommit != nil && p.HeadCommit.ID == p.After {
			ev.HeadTitle = model.FirstLine(p.HeadCommit.Message, model.HeadTitleMaxLen)
		}
	}
	return p.Repository.ID, []model.RepoEvent{ev}, nil
}

func parsePR(event string, body []byte) (int64, []model.RepoEvent, error) {
	var p struct {
		Action      string      `json:"action"`
		PullRequest *apiPR      `json:"pull_request"`
		Repository  payloadRepo `json:"repository"`
		Sender      payloadUser `json:"sender"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.PullRequest == nil {
		return 0, nil, errBadPayload(event)
	}
	if p.Repository.ID <= 0 {
		return 0, nil, nil
	}
	ev, ok := p.PullRequest.event(p.Sender.Login)
	if !ok {
		return 0, nil, nil
	}
	if event == "pull_request_review" {
		// Solo cambia la revisión pedida: el PR de un review no trae
		// «merged» y no debe pisar su estado.
		ev.Kind = model.RepoEventReview
	}
	return p.Repository.ID, []model.RepoEvent{ev}, nil
}

func parseCheckSuite(body []byte) (int64, []model.RepoEvent, error) {
	var p struct {
		CheckSuite *struct {
			ID         int64  `json:"id"`
			HeadBranch string `json:"head_branch"`
			HeadSHA    string `json:"head_sha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_suite"`
		Repository payloadRepo `json:"repository"`
		Sender     payloadUser `json:"sender"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.CheckSuite == nil {
		return 0, nil, errBadPayload("check_suite")
	}
	cs := p.CheckSuite
	if p.Repository.ID <= 0 || cs.ID <= 0 || !model.IsSHA(cs.HeadSHA) {
		return 0, nil, nil
	}
	branch := cs.HeadBranch
	if !model.ValidBranch(branch) {
		branch = ""
	}
	return p.Repository.ID, []model.RepoEvent{{
		Kind:     model.RepoEventCI,
		Actor:    p.Sender.Login,
		Branch:   branch,
		HeadSHA:  cs.HeadSHA,
		SuiteID:  strconv.FormatInt(cs.ID, 10),
		CIStatus: checkStatus(cs.Status, cs.Conclusion),
	}}, nil
}

// checkStatus traduce status y conclusion de un check suite.
func checkStatus(status, conclusion string) model.CIStatus {
	if status != "completed" {
		return model.CIStatusPending
	}
	switch conclusion {
	case "success", "neutral", "skipped":
		return model.CIStatusSuccess
	case "cancelled", "stale":
		return model.CIStatusCanceled
	default: // failure, timed_out, action_required, startup_failure
		return model.CIStatusFailed
	}
}

package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// GitHub webhook payload structs — only the fields we care about.

type githubPRPayload struct {
	Action      string        `json:"action"`
	Number      int           `json:"number"`
	PullRequest githubPR      `json:"pull_request"`
	Repository  githubRepo    `json:"repository"`
	Sender      githubUser    `json:"sender"`
}

type githubPR struct {
	Number            int           `json:"number"`
	Title             string        `json:"title"`
	State             string        `json:"state"`
	Head              githubRef     `json:"head"`
	Base              githubRef     `json:"base"`
	User              githubUser    `json:"user"`
	AuthorAssociation string        `json:"author_association"`
	Labels            []githubLabel `json:"labels"`
}

type githubRef struct {
	Ref  string     `json:"ref"`
	SHA  string     `json:"sha"`
	Repo githubRepo `json:"repo"` // contains clone_url — critical for fork PRs
}

type githubUser struct {
	Login string `json:"login"`
}

type githubRepo struct {
	CloneURL string `json:"clone_url"`
}

type githubLabel struct {
	Name string `json:"name"`
}

type githubCommentPayload struct {
	Action     string        `json:"action"`
	Issue      githubIssue   `json:"issue"`
	Comment    githubComment `json:"comment"`
	Repository githubRepo    `json:"repository"`
	// Sender is the actor who triggered the event — the commenter.
	// author_association here is their relationship to the repo.
	Sender     githubSender  `json:"sender"`
}

type githubSender struct {
	Login             string `json:"login"`
	// GitHub does not put author_association on sender — it's on the comment
	// or issue object. Kept here for forward compatibility.
}

type githubIssue struct {
	Number      int           `json:"number"`
	PullRequest *struct{}     `json:"pull_request"` // non-nil = is a PR
	// author_association on the issue reflects the ISSUE AUTHOR's association,
	// not the commenter's. Don't use this for comment trust checks.
	AuthorAssociation string        `json:"author_association"`
	Labels            []githubLabel `json:"labels"`
	Title             string        `json:"title"`
}

type githubComment struct {
	Body string     `json:"body"`
	User githubUser `json:"user"`
	// author_association is the commenter's relationship to the repo.
	// This is the authoritative field for comment trust evaluation.
	AuthorAssociation string `json:"author_association"`
}

func parseGitHub(r *http.Request, body []byte) (PREvent, bool, error) {
	eventType := r.Header.Get("X-GitHub-Event")

	switch eventType {
	case "pull_request":
		return parseGitHubPR(body)
	case "issue_comment":
		return parseGitHubComment(body)
	default:
		return PREvent{}, true, nil
	}
}

func parseGitHubPR(body []byte) (PREvent, bool, error) {
	var p githubPRPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PREvent{}, false, fmt.Errorf("parse github pull_request: %w", err)
	}

	switch p.Action {
	case "opened", "synchronize", "reopened", "closed", "labeled", "unlabeled":
	default:
		return PREvent{}, true, nil
	}

	labels := make([]string, len(p.PullRequest.Labels))
	for i, l := range p.PullRequest.Labels {
		labels[i] = l.Name
	}

	action := p.Action
	if action == "reopened" {
		action = "opened"
	}

	return PREvent{
		Action:            action,
		PRNumber:          p.PullRequest.Number,
		Branch:            p.PullRequest.Head.Ref,
		HeadSHA:           p.PullRequest.Head.SHA,
		Author:            p.PullRequest.User.Login,
		AuthorAssociation: p.PullRequest.AuthorAssociation,
		Title:             p.PullRequest.Title,
		Labels:            labels,
		// For fork PRs head.repo.clone_url is the fork — use that for the build.
		// For same-repo PRs head.repo.clone_url == base.repo.clone_url.
		CloneURL:          p.PullRequest.Head.Repo.CloneURL,
	}, false, nil
}

func parseGitHubComment(body []byte) (PREvent, bool, error) {
	var p githubCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PREvent{}, false, fmt.Errorf("parse github issue_comment: %w", err)
	}

	// Only comments on PRs
	if p.Issue.PullRequest == nil {
		return PREvent{}, true, nil
	}

	// Only new comments, not edits or deletes
	if p.Action != "created" {
		return PREvent{}, true, nil
	}

	labels := make([]string, len(p.Issue.Labels))
	for i, l := range p.Issue.Labels {
		labels[i] = l.Name
	}

	commentAssociation := p.Comment.AuthorAssociation

	return PREvent{
		Action:                   "comment",
		PRNumber:                 p.Issue.Number,
		Title:                    p.Issue.Title,
		Labels:                   labels,
		CommentBody:              p.Comment.Body,
		CommentAuthor:            p.Comment.User.Login,
		CommentAuthorAssociation: commentAssociation,
		// Debug: association value logged in HandleEvent
	}, false, nil
}

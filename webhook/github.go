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
	Number            int        `json:"number"`
	Title             string     `json:"title"`
	State             string     `json:"state"`
	Head              githubRef  `json:"head"`
	User              githubUser `json:"user"`
	AuthorAssociation string     `json:"author_association"`
	Labels            []githubLabel `json:"labels"`
}

type githubRef struct {
	Ref string `json:"ref"` // branch name
	SHA string `json:"sha"`
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
	Action  string       `json:"action"`
	Issue   githubIssue  `json:"issue"`
	Comment githubComment `json:"comment"`
	Repository githubRepo `json:"repository"`
}

type githubIssue struct {
	Number            int           `json:"number"`
	PullRequest       *struct{}     `json:"pull_request"` // non-nil means it's a PR
	AuthorAssociation string        `json:"author_association"`
	Labels            []githubLabel `json:"labels"`
	Title             string        `json:"title"`
}

type githubComment struct {
	Body              string `json:"body"`
	User              githubUser `json:"user"`
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
		// Not a PR-related event — skip silently
		return PREvent{}, true, nil
	}
}

func parseGitHubPR(body []byte) (PREvent, bool, error) {
	var p githubPRPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PREvent{}, false, fmt.Errorf("parse github pull_request: %w", err)
	}

	// Actions we care about
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
	// normalise "reopened" → "opened" — same handling
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
	}, false, nil
}

func parseGitHubComment(body []byte) (PREvent, bool, error) {
	var p githubCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PREvent{}, false, fmt.Errorf("parse github issue_comment: %w", err)
	}

	// Only care about comments on PRs (issue_comment fires on both issues and PRs)
	if p.Issue.PullRequest == nil {
		return PREvent{}, true, nil
	}

	// Only created comments, not edits/deletes
	if p.Action != "created" {
		return PREvent{}, true, nil
	}

	labels := make([]string, len(p.Issue.Labels))
	for i, l := range p.Issue.Labels {
		labels[i] = l.Name
	}

	return PREvent{
		Action:                   "comment",
		PRNumber:                 p.Issue.Number,
		Title:                    p.Issue.Title,
		Labels:                   labels,
		CommentBody:              p.Comment.Body,
		CommentAuthor:            p.Comment.User.Login,
		CommentAuthorAssociation: p.Comment.AuthorAssociation,
		// Note: issue_comment does not include head SHA or branch.
		// The GitRepo reconciler must fetch those from the platform API
		// if it decides to act on this comment.
	}, false, nil
}

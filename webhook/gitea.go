package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Gitea/Forgejo webhook payloads.
// Forgejo is a Gitea fork and maintains API compatibility —
// the same parser handles both.

type giteaPRPayload struct {
	Action      string      `json:"action"`
	Number      int         `json:"number"`
	PullRequest giteaPR     `json:"pull_request"`
	Repository  giteaRepo   `json:"repository"`
	Sender      giteaUser   `json:"sender"`
}

type giteaPR struct {
	Number int        `json:"number"`
	Title  string     `json:"title"`
	State  string     `json:"state"`
	Head   giteaRef   `json:"head"`
	User   giteaUser  `json:"user"`
	Labels []giteaLabel `json:"labels"`
}

type giteaRef struct {
	Ref string `json:"ref"` // branch name
	SHA string `json:"sha"`
}

type giteaUser struct {
	Login string `json:"login"`
}

type giteaRepo struct {
	CloneURL string `json:"clone_url"`
}

type giteaLabel struct {
	Name string `json:"name"`
}

type giteaCommentPayload struct {
	Action     string        `json:"action"`
	Comment    giteaComment  `json:"comment"`
	Issue      giteaIssue    `json:"issue"`
	Repository giteaRepo     `json:"repository"`
	Sender     giteaUser     `json:"sender"`
}

type giteaComment struct {
	Body string    `json:"body"`
	User giteaUser `json:"user"`
}

type giteaIssue struct {
	Number      int          `json:"number"`
	Title       string       `json:"title"`
	PullRequest *struct{}    `json:"pull_request"` // non-nil = is a PR
	Labels      []giteaLabel `json:"labels"`
}

// Gitea does not report author_association the way GitHub does.
// We default to MEMBER and let the policy layer evaluate.
const giteaDefaultAssociation = "MEMBER"

func parseGitea(r *http.Request, body []byte) (PREvent, bool, error) {
	eventType := r.Header.Get("X-Gitea-Event")
	if eventType == "" {
		// Forgejo uses the same header
		eventType = r.Header.Get("X-Forgejo-Event")
	}

	switch eventType {
	case "pull_request":
		return parseGiteaPR(body)
	case "issue_comment":
		return parseGiteaComment(body)
	default:
		return PREvent{}, true, nil
	}
}

func parseGiteaPR(body []byte) (PREvent, bool, error) {
	var p giteaPRPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PREvent{}, false, fmt.Errorf("parse gitea pull_request: %w", err)
	}

	var action string
	switch p.Action {
	case "opened", "reopened":
		action = "opened"
	case "synchronized":
		action = "synchronize"
	case "closed":
		action = "closed"
	case "label_updated":
		// Gitea fires label_updated instead of labeled/unlabeled
		action = "labeled"
	default:
		return PREvent{}, true, nil
	}

	labels := make([]string, len(p.PullRequest.Labels))
	for i, l := range p.PullRequest.Labels {
		labels[i] = l.Name
	}

	return PREvent{
		Action:            action,
		PRNumber:          p.PullRequest.Number,
		Branch:            p.PullRequest.Head.Ref,
		HeadSHA:           p.PullRequest.Head.SHA,
		Author:            p.PullRequest.User.Login,
		AuthorAssociation: giteaDefaultAssociation,
		Title:             p.PullRequest.Title,
		Labels:            labels,
	}, false, nil
}

func parseGiteaComment(body []byte) (PREvent, bool, error) {
	var p giteaCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PREvent{}, false, fmt.Errorf("parse gitea issue_comment: %w", err)
	}

	// Only comments on PRs
	if p.Issue.PullRequest == nil {
		return PREvent{}, true, nil
	}

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
		CommentAuthorAssociation: giteaDefaultAssociation,
	}, false, nil
}

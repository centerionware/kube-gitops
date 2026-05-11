package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// GitLab webhook payload structs — Merge Request and Note (comment) events.

type gitlabMRPayload struct {
	ObjectKind       string           `json:"object_kind"`
	ObjectAttributes gitlabMRAttrs    `json:"object_attributes"`
	User             gitlabUser       `json:"user"`
	Project          gitlabProject    `json:"project"`
	Labels           []gitlabLabel    `json:"labels"`
}

type gitlabMRAttrs struct {
	IID          int    `json:"iid"` // MR number within the project
	Title        string `json:"title"`
	State        string `json:"state"` // opened, closed, merged, locked
	Action       string `json:"action"` // open, close, merge, update, approved, etc.
	SourceBranch string `json:"source_branch"`
	LastCommit   struct {
		ID string `json:"id"`
	} `json:"last_commit"`
	AuthorID int `json:"author_id"`
}

type gitlabUser struct {
	Username string `json:"username"`
}

type gitlabProject struct {
	HTTPURLToRepo string `json:"http_url_to_repo"`
}

type gitlabLabel struct {
	Title string `json:"title"`
}

type gitlabNotePayload struct {
	ObjectKind       string         `json:"object_kind"`
	User             gitlabUser     `json:"user"`
	ProjectID        int            `json:"project_id"`
	Project          gitlabProject  `json:"project"`
	ObjectAttributes gitlabNote     `json:"object_attributes"`
	MergeRequest     *gitlabMRAttrs `json:"merge_request"`
}

type gitlabNote struct {
	Note             string `json:"note"`
	NoteableType     string `json:"noteable_type"` // "MergeRequest" when on an MR
	AuthorID         int    `json:"author_id"`
}

// GitLab does not provide an author_association equivalent.
// We map based on the access level reported in the event.
// For webhook payloads, GitLab omits this; we default to MEMBER
// and let the policy layer handle it conservatively.
const gitlabDefaultAssociation = "MEMBER"

func parseGitLab(r *http.Request, body []byte) (PREvent, bool, error) {
	eventType := r.Header.Get("X-Gitlab-Event")

	switch eventType {
	case "Merge Request Hook":
		return parseGitLabMR(body)
	case "Note Hook":
		return parseGitLabNote(body)
	default:
		return PREvent{}, true, nil
	}
}

func parseGitLabMR(body []byte) (PREvent, bool, error) {
	var p gitlabMRPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PREvent{}, false, fmt.Errorf("parse gitlab MR hook: %w", err)
	}

	if p.ObjectKind != "merge_request" {
		return PREvent{}, true, nil
	}

	var action string
	switch p.ObjectAttributes.Action {
	case "open", "reopen":
		action = "opened"
	case "update":
		action = "synchronize"
	case "close", "merge":
		action = "closed"
	default:
		return PREvent{}, true, nil
	}

	labels := make([]string, len(p.Labels))
	for i, l := range p.Labels {
		labels[i] = l.Title
	}

	return PREvent{
		Action:            action,
		PRNumber:          p.ObjectAttributes.IID,
		Branch:            p.ObjectAttributes.SourceBranch,
		HeadSHA:           p.ObjectAttributes.LastCommit.ID,
		Author:            p.User.Username,
		AuthorAssociation: gitlabDefaultAssociation,
		Title:             p.ObjectAttributes.Title,
		Labels:            labels,
	}, false, nil
}

func parseGitLabNote(body []byte) (PREvent, bool, error) {
	var p gitlabNotePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PREvent{}, false, fmt.Errorf("parse gitlab Note hook: %w", err)
	}

	// Only care about notes on merge requests
	if p.ObjectAttributes.NoteableType != "MergeRequest" || p.MergeRequest == nil {
		return PREvent{}, true, nil
	}

	return PREvent{
		Action:                   "comment",
		PRNumber:                 p.MergeRequest.IID,
		Title:                    p.MergeRequest.Title,
		Branch:                   p.MergeRequest.SourceBranch,
		HeadSHA:                  p.MergeRequest.LastCommit.ID,
		CommentBody:              p.ObjectAttributes.Note,
		CommentAuthor:            p.User.Username,
		CommentAuthorAssociation: gitlabDefaultAssociation,
	}, false, nil
}

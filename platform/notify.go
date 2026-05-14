// Package platform handles all outbound communication with git platforms:
// webhook registration, deregistration, PR comments, and commit statuses.
package platform

import (
	"context"
	"fmt"
	"strings"
)

// Notifier posts comments and commit statuses back to a git platform.
type Notifier interface {
	// PostComment posts a comment on the given PR number.
	PostComment(ctx context.Context, repoURL string, prNumber int, body string) error

	// SetCommitStatus sets a commit status on the given SHA.
	// state: "pending", "success", "failure", "error"
	SetCommitStatus(ctx context.Context, repoURL, sha, state, description, targetURL string) error

	// RegisterWebhook creates a webhook on the platform pointing at hookURL.
	// Returns the platform-assigned webhook ID (for later deletion).
	RegisterWebhook(ctx context.Context, repoURL, hookURL, secret string) (string, error)

	// DeregisterWebhook deletes the webhook with the given platform ID.
	DeregisterWebhook(ctx context.Context, repoURL, hookID string) error
}

// New returns the correct Notifier for the given platform and token.
// repoURL is used to derive the API base URL for self-hosted instances.
func New(platform, repoURL, token string) (Notifier, error) {
	switch platform {
	case "github":
		return &githubNotifier{token: token}, nil
	case "gitlab":
		base, _, err := gitlabBaseAndPath(repoURL)
		if err != nil {
			return nil, err
		}
		return &gitlabNotifier{token: token, baseURL: base}, nil
	case "gitea", "forgejo":
		base, _, _, err := giteaBaseOwnerRepo(repoURL)
		if err != nil {
			return nil, err
		}
		return &giteaNotifier{token: token, baseURL: base}, nil
	default:
		return nil, fmt.Errorf("unsupported platform: %s", platform)
	}
}

// ── URL helpers shared across platform files ──────────────────────────────────

func gitlabBaseAndPath(repoURL string) (baseURL, projectPath string, err error) {
	u := strings.TrimSuffix(repoURL, ".git")
	idx := strings.Index(u, "://")
	if idx < 0 {
		return "", "", fmt.Errorf("invalid repo URL: %s", repoURL)
	}
	rest := u[idx+3:]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return "", "", fmt.Errorf("cannot parse host from repo URL: %s", repoURL)
	}
	scheme := u[:idx]
	host := rest[:slash]
	path := strings.TrimPrefix(rest[slash:], "/")
	return scheme + "://" + host, path, nil
}

func giteaBaseOwnerRepo(repoURL string) (baseURL, owner, repo string, err error) {
	u := strings.TrimSuffix(repoURL, ".git")
	idx := strings.Index(u, "://")
	if idx < 0 {
		return "", "", "", fmt.Errorf("invalid repo URL: %s", repoURL)
	}
	rest := u[idx+3:]
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 {
		return "", "", "", fmt.Errorf("gitea URL must be https://<host>/<owner>/<repo>")
	}
	scheme := u[:idx]
	return scheme + "://" + parts[0], parts[1], parts[2], nil
}

func orgRepo(repoURL string) string {
	u := strings.TrimSuffix(repoURL, ".git")
	parts := strings.Split(u, "/")
	if len(parts) < 2 {
		return u
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

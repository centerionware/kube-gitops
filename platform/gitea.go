package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type giteaNotifier struct {
	token   string
	baseURL string
}

func (g *giteaNotifier) PostComment(ctx context.Context, repoURL string, prNumber int, body string) error {
	_, owner, repo, _ := giteaBaseOwnerRepo(repoURL)
	apiURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/issues/%d/comments",
		g.baseURL, owner, repo, prNumber)
	payload, _ := json.Marshal(map[string]string{"body": body})
	_, err := g.do(ctx, http.MethodPost, apiURL, payload)
	return err
}

func (g *giteaNotifier) SetCommitStatus(ctx context.Context, repoURL, sha, state, description, targetURL string) error {
	_, owner, repo, _ := giteaBaseOwnerRepo(repoURL)
	apiURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/statuses/%s",
		g.baseURL, owner, repo, sha)
	// Gitea states: pending, success, error, failure, warning
	giteaState := state
	if state == "failure" {
		giteaState = "failure"
	}
	payload, _ := json.Marshal(map[string]string{
		"state":       giteaState,
		"description": description,
		"target_url":  targetURL,
		"context":     "kube-gitops/preview",
	})
	_, err := g.do(ctx, http.MethodPost, apiURL, payload)
	return err
}

func (g *giteaNotifier) RegisterWebhook(ctx context.Context, repoURL, hookURL, secret string) (string, error) {
	_, owner, repo, _ := giteaBaseOwnerRepo(repoURL)
	apiURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/hooks", g.baseURL, owner, repo)
	payload, _ := json.Marshal(map[string]interface{}{
		"type":   "gitea",
		"active": true,
		"events": []string{"pull_request", "issue_comment"},
		"config": map[string]string{
			"url":          hookURL,
			"content_type": "json",
			"secret":       secret,
		},
	})
	body, err := g.do(ctx, http.MethodPost, apiURL, payload)
	if err != nil {
		return "", err
	}
	var resp struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse webhook response: %w", err)
	}
	return fmt.Sprintf("%d", resp.ID), nil
}

func (g *giteaNotifier) DeregisterWebhook(ctx context.Context, repoURL, hookID string) error {
	_, owner, repo, _ := giteaBaseOwnerRepo(repoURL)
	apiURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/hooks/%s",
		g.baseURL, owner, repo, hookID)
	_, err := g.do(ctx, http.MethodDelete, apiURL, nil)
	return err
}

func (g *giteaNotifier) do(ctx context.Context, method, apiURL string, body []byte) ([]byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiURL, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+g.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitea %s %s: %w", method, apiURL, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gitea %s %s: status %d: %s",
			method, apiURL, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

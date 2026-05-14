package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type gitlabNotifier struct {
	token   string
	baseURL string
}

func (g *gitlabNotifier) PostComment(ctx context.Context, repoURL string, prNumber int, body string) error {
	_, path, _ := gitlabBaseAndPath(repoURL)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/notes",
		g.baseURL, url.PathEscape(path), prNumber)
	payload, _ := json.Marshal(map[string]string{"body": body})
	_, err := g.do(ctx, http.MethodPost, apiURL, payload)
	return err
}

func (g *gitlabNotifier) SetCommitStatus(ctx context.Context, repoURL, sha, state, description, targetURL string) error {
	_, path, _ := gitlabBaseAndPath(repoURL)
	// GitLab states: pending, running, success, failed, canceled
	glState := state
	if state == "failure" || state == "error" {
		glState = "failed"
	}
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/statuses/%s",
		g.baseURL, url.PathEscape(path), sha)
	payload, _ := json.Marshal(map[string]string{
		"state":       glState,
		"description": description,
		"target_url":  targetURL,
		"name":        "kube-gitops/preview",
	})
	_, err := g.do(ctx, http.MethodPost, apiURL, payload)
	return err
}

func (g *gitlabNotifier) RegisterWebhook(ctx context.Context, repoURL, hookURL, secret string) (string, error) {
	_, path, _ := gitlabBaseAndPath(repoURL)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/hooks", g.baseURL, url.PathEscape(path))
	payload, _ := json.Marshal(map[string]interface{}{
		"url":                      hookURL,
		"token":                    secret,
		"merge_requests_events":    true,
		"note_events":              true,
		"enable_ssl_verification":  true,
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

func (g *gitlabNotifier) DeregisterWebhook(ctx context.Context, repoURL, hookID string) error {
	_, path, _ := gitlabBaseAndPath(repoURL)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/hooks/%s",
		g.baseURL, url.PathEscape(path), hookID)
	_, err := g.do(ctx, http.MethodDelete, apiURL, nil)
	return err
}

func (g *gitlabNotifier) do(ctx context.Context, method, apiURL string, body []byte) ([]byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiURL, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab %s %s: %w", method, apiURL, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gitlab %s %s: status %d: %s",
			method, apiURL, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

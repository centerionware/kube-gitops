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

type githubNotifier struct {
	token string
}

func (g *githubNotifier) PostComment(ctx context.Context, repoURL string, prNumber int, body string) error {
	or := orgRepo(repoURL)
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", or, prNumber)
	payload, _ := json.Marshal(map[string]string{"body": body})
	_, err := g.do(ctx, http.MethodPost, url, payload)
	return err
}

func (g *githubNotifier) SetCommitStatus(ctx context.Context, repoURL, sha, state, description, targetURL string) error {
	or := orgRepo(repoURL)
	url := fmt.Sprintf("https://api.github.com/repos/%s/statuses/%s", or, sha)
	payload, _ := json.Marshal(map[string]string{
		"state":       state,
		"description": description,
		"target_url":  targetURL,
		"context":     "kube-gitops/preview",
	})
	_, err := g.do(ctx, http.MethodPost, url, payload)
	return err
}

func (g *githubNotifier) RegisterWebhook(ctx context.Context, repoURL, hookURL, secret string) (string, error) {
	or := orgRepo(repoURL)
	url := fmt.Sprintf("https://api.github.com/repos/%s/hooks", or)
	payload, _ := json.Marshal(map[string]interface{}{
		"name":   "web",
		"active": true,
		"events": []string{"pull_request", "issue_comment"},
		"config": map[string]string{
			"url":          hookURL,
			"content_type": "json",
			"secret":       secret,
			"insecure_ssl": "0",
		},
	})
	body, err := g.do(ctx, http.MethodPost, url, payload)
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

func (g *githubNotifier) DeregisterWebhook(ctx context.Context, repoURL, hookID string) error {
	or := orgRepo(repoURL)
	url := fmt.Sprintf("https://api.github.com/repos/%s/hooks/%s", or, hookID)
	_, err := g.do(ctx, http.MethodDelete, url, nil)
	return err
}

func (g *githubNotifier) do(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github %s %s: status %d: %s",
			method, url, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

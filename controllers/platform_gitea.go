package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func fetchGiteaOpenPRs(ctx context.Context, repoURL, token string) ([]openPR, error) {
	baseURL, owner, repo, err := giteaBaseOwnerRepo(repoURL)
	if err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls?state=open&limit=50", baseURL, owner, repo)

	body, err := giteaGET(ctx, apiURL, token)
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Poster struct {
			Login string `json:"login"`
		} `json:"poster"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}

	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse gitea PRs: %w", err)
	}

	prs := make([]openPR, len(raw))
	for i, r := range raw {
		labels := make([]string, len(r.Labels))
		for j, l := range r.Labels {
			labels[j] = l.Name
		}
		prs[i] = openPR{
			Number:            r.Number,
			Title:             r.Title,
			Branch:            r.Head.Ref,
			HeadSHA:           r.Head.SHA,
			Author:            r.Poster.Login,
			AuthorAssociation: giteaDefaultAssociation,
			Labels:            labels,
		}
	}
	return prs, nil
}

func fetchGiteaPRHead(ctx context.Context, repoURL string, prNumber int, token string) (sha, branch string, err error) {
	baseURL, owner, repo, err := giteaBaseOwnerRepo(repoURL)
	if err != nil {
		return "", "", err
	}

	apiURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d", baseURL, owner, repo, prNumber)

	body, err := giteaGET(ctx, apiURL, token)
	if err != nil {
		return "", "", err
	}

	var raw struct {
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", "", fmt.Errorf("parse gitea PR head: %w", err)
	}
	return raw.Head.SHA, raw.Head.Ref, nil
}

func giteaGET(ctx context.Context, apiURL, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitea GET %s: %w", apiURL, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitea GET %s: status %d: %s", apiURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// giteaBaseOwnerRepo splits https://gitea.example.com/owner/repo into
// base URL, owner, and repo name.
func giteaBaseOwnerRepo(repoURL string) (baseURL, owner, repo string, err error) {
	u, err := url.Parse(strings.TrimSuffix(repoURL, ".git"))
	if err != nil {
		return "", "", "", fmt.Errorf("parse gitea repo URL: %w", err)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("gitea repo URL must be https://<host>/<owner>/<repo>")
	}
	return u.Scheme + "://" + u.Host, parts[0], parts[1], nil
}

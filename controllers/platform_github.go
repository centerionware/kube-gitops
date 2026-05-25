package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func fetchGitHubOpenPRs(ctx context.Context, repoURL, token string) ([]openPR, error) {
	or := orgRepo(repoURL)
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls?state=open&per_page=100", or)

	body, err := githubGET(ctx, url, token)
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Head   struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				CloneURL string `json:"clone_url"`
			} `json:"repo"`
		} `json:"head"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		AuthorAssociation string `json:"author_association"`
		Labels            []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}

	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse github PRs: %w", err)
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
			CloneURL:          r.Head.Repo.CloneURL,
			Author:            r.User.Login,
			AuthorAssociation: r.AuthorAssociation,
			Labels:            labels,
		}
	}
	return prs, nil
}

func fetchGitHubPRHead(ctx context.Context, repoURL string, prNumber int, token string) (sha, branch string, err error) {
	or := orgRepo(repoURL)
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", or, prNumber)

	body, err := githubGET(ctx, url, token)
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
		return "", "", fmt.Errorf("parse github PR head: %w", err)
	}
	return raw.Head.SHA, raw.Head.Ref, nil
}

func githubGET(ctx context.Context, url, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github GET %s: status %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

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

const gitlabDefaultAssociation = "MEMBER"

func fetchGitLabOpenMRs(ctx context.Context, repoURL, token string) ([]openPR, error) {
	baseURL, projectPath, err := gitlabBaseAndPath(repoURL)
	if err != nil {
		return nil, err
	}

	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests?state=opened&per_page=100", baseURL, encoded)

	body, err := gitlabGET(ctx, apiURL, token)
	if err != nil {
		return nil, err
	}

	var raw []struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		SourceBranch string `json:"source_branch"`
		SHA          string `json:"sha"`
		Author       struct {
			Username string `json:"username"`
		} `json:"author"`
		Labels []string `json:"labels"`
	}

	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse gitlab MRs: %w", err)
	}

	prs := make([]openPR, len(raw))
	for i, r := range raw {
		prs[i] = openPR{
			Number:            r.IID,
			Title:             r.Title,
			Branch:            r.SourceBranch,
			HeadSHA:           r.SHA,
			Author:            r.Author.Username,
			AuthorAssociation: gitlabDefaultAssociation,
			Labels:            r.Labels,
		}
	}
	return prs, nil
}

func fetchGitLabMRHead(ctx context.Context, repoURL string, prNumber int, token string) (sha, branch string, err error) {
	baseURL, projectPath, err := gitlabBaseAndPath(repoURL)
	if err != nil {
		return "", "", err
	}

	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d", baseURL, encoded, prNumber)

	body, err := gitlabGET(ctx, apiURL, token)
	if err != nil {
		return "", "", err
	}

	var raw struct {
		SHA          string `json:"sha"`
		SourceBranch string `json:"source_branch"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", "", fmt.Errorf("parse gitlab MR head: %w", err)
	}
	return raw.SHA, raw.SourceBranch, nil
}

func gitlabGET(ctx context.Context, apiURL, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab GET %s: %w", apiURL, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab GET %s: status %d: %s", apiURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// gitlabBaseAndPath splits https://gitlab.example.com/org/repo into
// base URL and "org/repo" project path.
func gitlabBaseAndPath(repoURL string) (baseURL, projectPath string, err error) {
	u, err := url.Parse(strings.TrimSuffix(repoURL, ".git"))
	if err != nil {
		return "", "", fmt.Errorf("parse gitlab repo URL: %w", err)
	}
	// path is /org/repo — drop leading slash
	projectPath = strings.TrimPrefix(u.Path, "/")
	baseURL = u.Scheme + "://" + u.Host
	return baseURL, projectPath, nil
}

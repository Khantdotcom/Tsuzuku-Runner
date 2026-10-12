package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

type gitHubClient struct {
	token  string
	base   string
	client *http.Client
}

func newGitHubClient(token, base string, hc *http.Client) *gitHubClient {
	if base == "" {
		base = "https://api.github.com"
	}
	base = strings.TrimRight(base, "/")
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &gitHubClient{token: token, base: base, client: hc}
}

func (c *gitHubClient) openPatchPR(
	ctx context.Context,
	owner, repo, revision, branch, path string,
	patch []byte,
	jobNumber int64,
	spec workload.Spec,
) (prURL string, err error) {
	commit, err := c.getCommit(ctx, owner, repo, revision)
	if err != nil {
		return "", err
	}
	blobSHA, err := c.createBlob(ctx, owner, repo, patch)
	if err != nil {
		return "", err
	}
	treeSHA, err := c.createTree(ctx, owner, repo, commit.TreeSHA, path, blobSHA)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("tsuzuku: add patch for job #%d", jobNumber)
	newCommit, err := c.createCommit(ctx, owner, repo, msg, treeSHA, commit.SHA)
	if err != nil {
		return "", err
	}
	if err := c.ensureBranch(ctx, owner, repo, branch, newCommit); err != nil {
		return "", err
	}
	title := fmt.Sprintf("tsuzuku: job #%d", jobNumber)
	body := prBody(jobNumber, path, spec)
	return c.createOrFindPR(ctx, owner, repo, branch, commit.SHA, title, body)
}

type ghCommit struct {
	SHA     string
	TreeSHA string
}

func (c *gitHubClient) getCommit(ctx context.Context, owner, repo, ref string) (ghCommit, error) {
	var raw struct {
		SHA    string `json:"sha"`
		Commit struct {
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		} `json:"commit"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/commits/%s", owner, repo, ref), nil, &raw); err != nil {
		return ghCommit{}, fmt.Errorf("resolve revision %q: %w", ref, err)
	}
	return ghCommit{SHA: raw.SHA, TreeSHA: raw.Commit.Tree.SHA}, nil
}

func (c *gitHubClient) createBlob(ctx context.Context, owner, repo string, content []byte) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/blobs", owner, repo), map[string]any{
		"content":  string(content),
		"encoding": "utf-8",
	}, &out)
	if err != nil {
		return "", fmt.Errorf("create blob: %w", err)
	}
	return out.SHA, nil
}

func (c *gitHubClient) createTree(ctx context.Context, owner, repo, baseTree, path, blobSHA string) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/trees", owner, repo), map[string]any{
		"base_tree": baseTree,
		"tree": []map[string]any{{
			"path": path, "mode": "100644", "type": "blob", "sha": blobSHA,
		}},
	}, &out)
	if err != nil {
		return "", fmt.Errorf("create tree: %w", err)
	}
	return out.SHA, nil
}

func (c *gitHubClient) createCommit(ctx context.Context, owner, repo, message, treeSHA, parentSHA string) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/commits", owner, repo), map[string]any{
		"message": message,
		"tree":    treeSHA,
		"parents": []string{parentSHA},
	}, &out)
	if err != nil {
		return "", fmt.Errorf("create commit: %w", err)
	}
	return out.SHA, nil
}

func (c *gitHubClient) ensureBranch(ctx context.Context, owner, repo, branch, sha string) error {
	ref := "refs/heads/" + branch
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo), map[string]any{
		"ref": ref, "sha": sha,
	}, nil)
	if err == nil {
		return nil
	}
	// Branch may already exist from a prior attempt: move it to the new commit.
	if err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, branch), map[string]any{
		"sha": sha, "force": true,
	}, nil); err != nil {
		return fmt.Errorf("update branch %s: %w", branch, err)
	}
	return nil
}

func (c *gitHubClient) createOrFindPR(ctx context.Context, owner, repo, headBranch, baseSHA, title, body string) (string, error) {
	baseBranch, err := c.defaultBranch(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	_ = baseSHA
	var created struct {
		HTMLURL string `json:"html_url"`
	}
	err = c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", owner, repo), map[string]any{
		"title": title,
		"head":  headBranch,
		"base":  baseBranch,
		"body":  body,
	}, &created)
	if err == nil {
		return created.HTMLURL, nil
	}
	// Duplicate PR: list open PRs for this head.
	var list []struct {
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if listErr := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls?state=open&head=%s:%s", owner, repo, owner, headBranch), nil, &list); listErr != nil {
		return "", fmt.Errorf("create pull request: %w", err)
	}
	for _, pr := range list {
		if pr.Head.Ref == headBranch && pr.HTMLURL != "" {
			return pr.HTMLURL, nil
		}
	}
	return "", fmt.Errorf("create pull request: %w", err)
}

func (c *gitHubClient) defaultBranch(ctx context.Context, owner, repo string) (string, error) {
	var raw struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s", owner, repo), nil, &raw); err != nil {
		return "", fmt.Errorf("get repository: %w", err)
	}
	if raw.DefaultBranch == "" {
		return "main", nil
	}
	return raw.DefaultBranch, nil
}

func (c *gitHubClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		return fmt.Errorf("GitHub %s %s: %s (%s)", method, path, res.Status, msg)
	}
	if out == nil || len(data) == 0 || string(data) == "null" {
		return nil
	}
	return json.Unmarshal(data, out)
}

func prBody(jobNumber int64, path string, spec workload.Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Opened by [Tsuzuku Runner](https://github.com/Khantdotcom/tsuzuku-runner) for job #%d.\n\n", jobNumber)
	fmt.Fprintf(&b, "This pull request adds `%s` with the workspace diff after a verified run.\n\n", path)
	fmt.Fprintf(&b, "- Repository revision: `%s`\n", spec.Revision)
	fmt.Fprintf(&b, "- Command: `%s`\n", spec.Command)
	if len(spec.AcceptanceCriteria) > 0 {
		b.WriteString("\n### Acceptance criteria\n\n")
		for _, c := range spec.AcceptanceCriteria {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	b.WriteString("\nApply locally with:\n\n```bash\ngit apply " + path + "\n```\n")
	return b.String()
}

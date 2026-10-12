package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

func TestParseGitHubRepo(t *testing.T) {
	owner, repo, err := parseGitHubRepo("https://github.com/Acme/app.git")
	if err != nil || owner != "Acme" || repo != "app" {
		t.Fatalf("got %s/%s %v", owner, repo, err)
	}
	if _, _, err := parseGitHubRepo("https://gitlab.com/a/b"); err == nil {
		t.Fatal("expected error for non-GitHub URL")
	}
}

func TestOpenPatchPRIdempotentOnExistingPR(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/commits/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha": "abc", "commit": map[string]any{"tree": map[string]string{"sha": "tree1"}},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/blobs"):
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "blob1"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/trees"):
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "tree2"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/commits"):
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "commit2"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodGet && !strings.Contains(r.URL.Path, "/pulls") && strings.Count(r.URL.Path, "/") == 3:
			_ = json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			posts++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Validation Failed"}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"html_url": "https://github.com/o/r/pull/7",
				"head":     map[string]string{"ref": "tsuzuku/job-1"},
			}})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c := newGitHubClient("token", srv.URL, srv.Client())
	url, err := c.openPatchPR(context.Background(), "o", "r", "main", "tsuzuku/job-1", "tsuzuku/job-1.patch",
		[]byte("diff"), 1, workload.Spec{Revision: "main", Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/o/r/pull/7" || posts != 1 {
		t.Fatalf("url=%q posts=%d", url, posts)
	}
}

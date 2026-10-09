package api

import (
	"net/http"
	"testing"
)

func TestHealthz(t *testing.T) {
	rec := serve(t, newTestRouter(nil, nil), http.MethodGet, "/healthz", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if got, want := rec.Body.String(), `{"status":"ok"}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestHealthzRejectsWrongMethod(t *testing.T) {
	rec := serve(t, newTestRouter(nil, nil), http.MethodPost, "/healthz", "", "")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	decodeProblem(t, rec)
}

func TestUnknownRouteReturnsProblem(t *testing.T) {
	rec := serve(t, newTestRouter(nil, nil), http.MethodGet, "/nope", "", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if p := decodeProblem(t, rec); p.Instance != "/nope" {
		t.Errorf("instance = %q, want /nope", p.Instance)
	}
}

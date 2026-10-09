package api

import (
	"errors"
	"net/http"
	"testing"
)

func TestReadyz(t *testing.T) {
	rec := serve(t, newTestRouter(nil, fakePinger{}), http.MethodGet, "/readyz", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "{\"status\":\"ready\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestReadyzReportsDatabaseDown(t *testing.T) {
	pinger := fakePinger{err: errors.New("connection refused")}
	rec := serve(t, newTestRouter(nil, pinger), http.MethodGet, "/readyz", "", "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if p := decodeProblem(t, rec); p.Detail != "database is unreachable" {
		t.Errorf("detail = %q", p.Detail)
	}
}

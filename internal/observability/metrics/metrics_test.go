package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestHandlerExposesCollectors(t *testing.T) {
	reg := New()
	reg.ObserveJob("submit")
	reg.SetQueue(1, 0, 2, 3, 0, 0)

	r := chi.NewRouter()
	r.Use(reg.Middleware)
	r.Get("/hello", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Handle("/metrics", Handler())

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/hello", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("hello status %d", rr.Code)
	}

	mreq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	mrr := httptest.NewRecorder()
	r.ServeHTTP(mrr, mreq)
	body := mrr.Body.String()
	for _, want := range []string{
		"tsuzuku_jobs_total",
		"tsuzuku_queue_depth",
		"tsuzuku_http_requests_total",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

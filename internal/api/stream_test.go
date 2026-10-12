package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
)

func TestStreamEvents(t *testing.T) {
	jobID := uuid.New()
	jobs := &fakeJobService{
		job: db.Job{ID: jobID, Number: 7, State: "COMPLETED", WorkloadID: uuid.New(), CreatedAt: time.Now()},
		events: []db.JobEvent{
			{ID: 11, Type: "job.created", Payload: json.RawMessage(`{}`), CreatedAt: time.Now()},
		},
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/jobs/7/events/stream", nil)
	rec := httptest.NewRecorder()
	newJobsRouter(jobs).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: event") || !strings.Contains(body, "id: 11") || !strings.Contains(body, "event: done") {
		t.Fatalf("stream = %q", body)
	}
	if jobs.afters[0] != 0 {
		t.Errorf("first after = %d, want 0", jobs.afters[0])
	}
}

func TestStreamLogsUsesLastEventID(t *testing.T) {
	jobs := &fakeJobService{
		job:    db.Job{ID: uuid.New(), Number: 7, State: "COMPLETED", WorkloadID: uuid.New(), CreatedAt: time.Now()},
		chunks: []db.LogChunk{{ID: 42, Seq: 1, Stream: "stdout", Data: []byte("hi"), CreatedAt: time.Now()}},
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/jobs/7/logs/stream", nil)
	req.Header.Set("Last-Event-ID", "41")
	rec := httptest.NewRecorder()
	newJobsRouter(jobs).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if jobs.afters[0] != 41 {
		t.Errorf("after = %d, want 41", jobs.afters[0])
	}
	scanner := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	var sawChunk bool
	for scanner.Scan() {
		if scanner.Text() == "event: chunk" {
			sawChunk = true
		}
	}
	if !sawChunk {
		t.Fatalf("missing chunk event in %q", rec.Body.String())
	}
}

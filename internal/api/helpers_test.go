package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
)

const (
	testToken      = "test-worker-token"
	testStaleAfter = 15 * time.Second
)

type fakePinger struct{ err error }

func (p fakePinger) Ping(context.Context) error { return p.err }

type fakeWorkerStore struct {
	mu            sync.Mutex
	upserts       []db.UpsertWorkerParams
	upsertErr     error
	heartbeats    []db.RecordHeartbeatParams
	heartbeatRows int64
	heartbeatErr  error
	rows          []db.ListWorkersRow
	listErr       error
	missing       bool
	existsErr     error
	cancel        []uuid.UUID
	cancelErr     error
}

func (f *fakeWorkerStore) ListCancelRequestedAttempts(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	if f.cancel == nil {
		return []uuid.UUID{}, f.cancelErr
	}
	return f.cancel, f.cancelErr
}

func (f *fakeWorkerStore) WorkerExists(context.Context, uuid.UUID) (bool, error) {
	return !f.missing, f.existsErr
}

func (f *fakeWorkerStore) UpsertWorker(_ context.Context, arg db.UpsertWorkerParams) (db.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, arg)
	if f.upsertErr != nil {
		return db.Worker{}, f.upsertErr
	}
	return db.Worker{
		ID:        arg.ID,
		Name:      arg.Name,
		Slots:     arg.Slots,
		CpuMillis: arg.CpuMillis,
		MemoryMB:  arg.MemoryMB,
		Metadata:  arg.Metadata,
	}, nil
}

func (f *fakeWorkerStore) RecordHeartbeat(_ context.Context, arg db.RecordHeartbeatParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats = append(f.heartbeats, arg)
	return f.heartbeatRows, f.heartbeatErr
}

func (f *fakeWorkerStore) ListWorkers(context.Context) ([]db.ListWorkersRow, error) {
	return f.rows, f.listErr
}

func newTestRouter(store *fakeWorkerStore, pinger Pinger) http.Handler {
	if store == nil {
		store = &fakeWorkerStore{}
	}
	if pinger == nil {
		pinger = fakePinger{}
	}
	return NewRouter(Config{
		Logger:           slog.New(slog.DiscardHandler),
		DB:               pinger,
		Workers:          store,
		WorkerToken:      testToken,
		WorkerStaleAfter: testStaleAfter,
	})
}

// serve sends a request through h. An empty token sends no Authorization header.
func serve(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) problem {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", got)
	}
	var p problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Status != rec.Code {
		t.Errorf("problem status = %d, want %d", p.Status, rec.Code)
	}
	return p
}

//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Khantdotcom/tsuzuku-runner/internal/api"
	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/scheduler"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/storetest"
	"github.com/Khantdotcom/tsuzuku-runner/internal/worker"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

const token = "integration-token"

func TestMain(m *testing.M) {
	storetest.Main(m)
}

type listedWorker struct {
	ID             uuid.UUID `json:"id"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	Slots          int       `json:"slots"`
	CPUUsedPercent *float64  `json:"cpu_used_percent"`
	MemoryUsedMB   *int      `json:"memory_used_mb"`
}

func newServer(t *testing.T, staleAfter time.Duration) *httptest.Server {
	t.Helper()
	srv, _, _ := newServerWithPool(t, staleAfter)
	return srv
}

func newServerWithPool(t *testing.T, staleAfter time.Duration) (*httptest.Server, *pgxpool.Pool, *scheduler.Notifier) {
	t.Helper()
	pool := storetest.NewPool(t)
	jobs := job.NewService(pool)
	notifier := scheduler.NewNotifier()
	srv := httptest.NewServer(api.NewRouter(api.Config{
		Logger:           slog.New(slog.DiscardHandler),
		DB:               pool,
		Workers:          db.New(pool),
		Jobs:             jobs,
		Claimer:          jobs,
		Attempts:         jobs,
		Assignments:      notifier,
		WorkerToken:      token,
		WorkerStaleAfter: staleAfter,
		WorkloadLimits:   workload.DefaultLimits(),
	}))
	t.Cleanup(srv.Close)
	return srv, pool, notifier
}

// post sends body as JSON with the worker token, decodes a 200 response into
// out when out is non-nil, and returns the status code.
func post(t *testing.T, url string, body, out any) int {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func listWorkers(t *testing.T, baseURL string) []listedWorker {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/workers", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list workers status = %d", resp.StatusCode)
	}
	var body struct {
		Workers []listedWorker `json:"workers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Workers
}

func TestWorkerLifecycle(t *testing.T) {
	srv := newServer(t, time.Second)
	register := workerapi.RegisterRequest{Name: "lifecycle", Slots: 2, CPUMillis: 2000, MemoryMB: 4096}

	var reg workerapi.RegisterResponse
	if status := post(t, srv.URL+workerapi.RegisterPath, register, &reg); status != http.StatusOK {
		t.Fatalf("register status = %d", status)
	}

	heartbeat := workerapi.HeartbeatRequest{CPUUsedPercent: 12.5, MemoryUsedMB: 512}
	if status := post(t, srv.URL+workerapi.HeartbeatPath(reg.ID), heartbeat, nil); status != http.StatusNoContent {
		t.Fatalf("heartbeat status = %d", status)
	}

	workers := listWorkers(t, srv.URL)
	if len(workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(workers))
	}
	w := workers[0]
	if w.ID != reg.ID || w.Status != "online" || w.Slots != 2 {
		t.Errorf("worker = %+v, want online with id %s", w, reg.ID)
	}
	if w.CPUUsedPercent == nil || *w.CPUUsedPercent != 12.5 || w.MemoryUsedMB == nil || *w.MemoryUsedMB != 512 {
		t.Errorf("usage = %v / %v", w.CPUUsedPercent, w.MemoryUsedMB)
	}

	register.Slots = 4
	var again workerapi.RegisterResponse
	if status := post(t, srv.URL+workerapi.RegisterPath, register, &again); status != http.StatusOK {
		t.Fatalf("re-register status = %d", status)
	}
	if again.ID != reg.ID {
		t.Errorf("re-register id = %s, want %s", again.ID, reg.ID)
	}

	unknown := workerapi.HeartbeatPath(uuid.Must(uuid.NewV7()))
	if status := post(t, srv.URL+unknown, workerapi.HeartbeatRequest{}, nil); status != http.StatusNotFound {
		t.Errorf("unknown heartbeat status = %d, want 404", status)
	}

	time.Sleep(1500 * time.Millisecond)
	if w := listWorkers(t, srv.URL)[0]; w.Status != "offline" || w.Slots != 4 {
		t.Errorf("after stale window: status %s slots %d, want offline with 4 slots", w.Status, w.Slots)
	}
}

func TestAgentAgainstServer(t *testing.T) {
	srv := newServer(t, 15*time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	agent := worker.NewAgent(worker.NewClient(srv.URL, token), worker.HostProbe{}, slog.New(slog.DiscardHandler), worker.Options{
		Name:              "agent-e2e",
		Slots:             2,
		HeartbeatInterval: 20 * time.Millisecond,
		Version:           "test",
	})
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		workers := listWorkers(t, srv.URL)
		if len(workers) == 1 && workers[0].Status == "online" && workers[0].MemoryUsedMB != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker never reported usage: %+v", workers)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop")
	}
}

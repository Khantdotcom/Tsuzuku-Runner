package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

const validRegisterBody = `{"name":"worker-1","slots":2,"cpu_millis":4000,"memory_mb":8192,` +
	`"metadata":{"os":"linux","arch":"amd64","hostname":"box","version":"dev"}}`

func TestRegisterWorker(t *testing.T) {
	store := &fakeWorkerStore{}
	rec := serve(t, newTestRouter(store, nil), http.MethodPost, workerapi.RegisterPath, validRegisterBody, testToken)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body)
	}
	var resp workerapi.RegisterResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	got := store.upserts[0]
	if resp.ID != got.ID || resp.Name != "worker-1" {
		t.Errorf("response = %+v, want id %s name worker-1", resp, got.ID)
	}
	if got.ID.Version() != 7 {
		t.Errorf("id version = %d, want 7", got.ID.Version())
	}
	if got.Slots != 2 || got.CpuMillis != 4000 || got.MemoryMB != 8192 {
		t.Errorf("capacity = %+v", got)
	}
	var meta workerapi.Metadata
	if err := json.Unmarshal(got.Metadata, &meta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if meta.OS != "linux" || meta.Hostname != "box" {
		t.Errorf("metadata = %+v", meta)
	}
}

func TestRegisterWorkerRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		token string
		want  int
	}{
		{"missing token", validRegisterBody, "", http.StatusUnauthorized},
		{"wrong token", validRegisterBody, "wrong", http.StatusUnauthorized},
		{"malformed json", `{"name":`, testToken, http.StatusBadRequest},
		{"trailing data", validRegisterBody + `{}`, testToken, http.StatusBadRequest},
		{"empty name", `{"name":"","slots":1,"cpu_millis":1,"memory_mb":1}`, testToken, http.StatusUnprocessableEntity},
		{"bad name", `{"name":"-x y","slots":1,"cpu_millis":1,"memory_mb":1}`, testToken, http.StatusUnprocessableEntity},
		{"zero slots", `{"name":"w","slots":0,"cpu_millis":1,"memory_mb":1}`, testToken, http.StatusUnprocessableEntity},
		{"too many slots", `{"name":"w","slots":257,"cpu_millis":1,"memory_mb":1}`, testToken, http.StatusUnprocessableEntity},
		{"zero cpu", `{"name":"w","slots":1,"cpu_millis":0,"memory_mb":1}`, testToken, http.StatusUnprocessableEntity},
		{"huge memory", `{"name":"w","slots":1,"cpu_millis":1,"memory_mb":3000000000}`, testToken, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeWorkerStore{}
			rec := serve(t, newTestRouter(store, nil), http.MethodPost, workerapi.RegisterPath, tt.body, tt.token)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
			decodeProblem(t, rec)
			if len(store.upserts) != 0 {
				t.Errorf("store was called %d times", len(store.upserts))
			}
		})
	}
}

func TestRegisterWorkerStoreError(t *testing.T) {
	store := &fakeWorkerStore{upsertErr: errors.New("boom")}
	rec := serve(t, newTestRouter(store, nil), http.MethodPost, workerapi.RegisterPath, validRegisterBody, testToken)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if p := decodeProblem(t, rec); p.Detail != "" {
		t.Errorf("detail leaks internals: %q", p.Detail)
	}
}

func TestHeartbeat(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	store := &fakeWorkerStore{heartbeatRows: 1}
	body := `{"cpu_used_percent":37.5,"memory_used_mb":2048}`
	rec := serve(t, newTestRouter(store, nil), http.MethodPost, workerapi.HeartbeatPath(id), body, testToken)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"cancel_attempts":[]}` {
		t.Errorf("body = %s, want an empty cancel list", got)
	}
	if len(store.heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(store.heartbeats))
	}
	got := store.heartbeats[0]
	if got.ID != id || *got.CpuUsedPercent != 37.5 || *got.MemoryUsedMB != 2048 {
		t.Errorf("heartbeat = id %s cpu %v mem %v", got.ID, *got.CpuUsedPercent, *got.MemoryUsedMB)
	}
}

func TestHeartbeatListsCancelledAttempts(t *testing.T) {
	attempt := uuid.Must(uuid.NewV7())
	store := &fakeWorkerStore{heartbeatRows: 1, cancel: []uuid.UUID{attempt}}
	rec := serve(t, newTestRouter(store, nil), http.MethodPost, workerapi.HeartbeatPath(uuid.Must(uuid.NewV7())),
		`{"cpu_used_percent":1,"memory_used_mb":1}`, testToken)

	var resp workerapi.HeartbeatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d, err %v", rec.Code, err)
	}
	if len(resp.CancelAttempts) != 1 || resp.CancelAttempts[0] != attempt {
		t.Errorf("cancel attempts = %v", resp.CancelAttempts)
	}
}

func TestHeartbeatUnknownWorker(t *testing.T) {
	store := &fakeWorkerStore{heartbeatRows: 0}
	path := workerapi.HeartbeatPath(uuid.Must(uuid.NewV7()))
	rec := serve(t, newTestRouter(store, nil), http.MethodPost, path, `{"cpu_used_percent":1,"memory_used_mb":1}`, testToken)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	decodeProblem(t, rec)
}

func TestHeartbeatRejectsBadRequests(t *testing.T) {
	valid := workerapi.HeartbeatPath(uuid.Must(uuid.NewV7()))
	tests := []struct {
		name  string
		path  string
		body  string
		token string
		want  int
	}{
		{"missing token", valid, `{"cpu_used_percent":1,"memory_used_mb":1}`, "", http.StatusUnauthorized},
		{"bad id", "/api/v1/workers/not-a-uuid/heartbeat", `{"cpu_used_percent":1,"memory_used_mb":1}`, testToken, http.StatusBadRequest},
		{"malformed json", valid, `nope`, testToken, http.StatusBadRequest},
		{"cpu above 100", valid, `{"cpu_used_percent":100.1,"memory_used_mb":1}`, testToken, http.StatusUnprocessableEntity},
		{"negative cpu", valid, `{"cpu_used_percent":-1,"memory_used_mb":1}`, testToken, http.StatusUnprocessableEntity},
		{"negative memory", valid, `{"cpu_used_percent":1,"memory_used_mb":-1}`, testToken, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeWorkerStore{heartbeatRows: 1}
			rec := serve(t, newTestRouter(store, nil), http.MethodPost, tt.path, tt.body, tt.token)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
			decodeProblem(t, rec)
			if len(store.heartbeats) != 0 {
				t.Errorf("store was called %d times", len(store.heartbeats))
			}
		})
	}
}

func TestListWorkers(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cpu := 12.5
	mem := int32(1024)
	row := func(name string, sinceHeartbeat time.Duration) db.ListWorkersRow {
		return db.ListWorkersRow{
			Worker: db.Worker{
				ID:              uuid.Must(uuid.NewV7()),
				Name:            name,
				Slots:           2,
				CpuMillis:       4000,
				MemoryMB:        8192,
				CpuUsedPercent:  &cpu,
				MemoryUsedMB:    &mem,
				Metadata:        json.RawMessage(`{"os":"linux"}`),
				RegisteredAt:    now.Add(-time.Hour),
				LastHeartbeatAt: now.Add(-sinceHeartbeat),
			},
			DBNow: now,
		}
	}
	store := &fakeWorkerStore{rows: []db.ListWorkersRow{
		row("fresh", 2*time.Second),
		row("boundary", testStaleAfter),
		row("stale", testStaleAfter+time.Millisecond),
	}}

	rec := serve(t, newTestRouter(store, nil), http.MethodGet, "/api/v1/workers", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp struct {
		Workers []workerView `json:"workers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]string{"fresh": workerOnline, "boundary": workerOnline, "stale": workerOffline}
	if len(resp.Workers) != len(want) {
		t.Fatalf("workers = %d, want %d", len(resp.Workers), len(want))
	}
	for _, w := range resp.Workers {
		if w.Status != want[w.Name] {
			t.Errorf("%s status = %q, want %q", w.Name, w.Status, want[w.Name])
		}
		if w.CPUUsedPercent == nil || *w.CPUUsedPercent != cpu || w.MemoryUsedMB == nil || *w.MemoryUsedMB != mem {
			t.Errorf("%s usage = %v / %v", w.Name, w.CPUUsedPercent, w.MemoryUsedMB)
		}
	}
}

func TestListWorkersEmpty(t *testing.T) {
	rec := serve(t, newTestRouter(&fakeWorkerStore{}, nil), http.MethodGet, "/api/v1/workers", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "{\"workers\":[]}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestListWorkersStoreError(t *testing.T) {
	store := &fakeWorkerStore{listErr: errors.New("boom")}
	rec := serve(t, newTestRouter(store, nil), http.MethodGet, "/api/v1/workers", "", "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	decodeProblem(t, rec)
}

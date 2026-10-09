package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

const (
	maxBodyBytes      = 64 << 10
	maxWorkerNameLen  = 128
	maxWorkerSlots    = 256
	workerOnline      = "online"
	workerOffline     = "offline"
	invalidWorkerBody = "request body must be a JSON object"
)

var workerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// WorkerStore is the persistence the worker endpoints need. *db.Queries
// implements it.
type WorkerStore interface {
	UpsertWorker(ctx context.Context, arg db.UpsertWorkerParams) (db.Worker, error)
	RecordHeartbeat(ctx context.Context, arg db.RecordHeartbeatParams) (int64, error)
	ListWorkers(ctx context.Context) ([]db.ListWorkersRow, error)
}

// workerView is the public representation of a worker.
type workerView struct {
	ID              uuid.UUID       `json:"id"`
	Name            string          `json:"name"`
	Status          string          `json:"status"`
	Slots           int32           `json:"slots"`
	CPUMillis       int32           `json:"cpu_millis"`
	MemoryMB        int32           `json:"memory_mb"`
	CPUUsedPercent  *float64        `json:"cpu_used_percent"`
	MemoryUsedMB    *int32          `json:"memory_used_mb"`
	Metadata        json.RawMessage `json:"metadata"`
	RegisteredAt    time.Time       `json:"registered_at"`
	LastHeartbeatAt time.Time       `json:"last_heartbeat_at"`
}

func (s *server) handleRegisterWorker(w http.ResponseWriter, r *http.Request) {
	var req workerapi.RegisterRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateRegister(req); err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	metadata, err := json.Marshal(req.Metadata)
	if err != nil {
		s.internalError(w, r, "encode worker metadata", err)
		return
	}
	id, err := uuid.NewV7()
	if err != nil {
		s.internalError(w, r, "generate worker id", err)
		return
	}

	worker, err := s.workers.UpsertWorker(r.Context(), db.UpsertWorkerParams{
		ID:        id,
		Name:      req.Name,
		Slots:     int32(req.Slots),     //nolint:gosec // bounded by validateRegister
		CpuMillis: int32(req.CPUMillis), //nolint:gosec // bounded by validateRegister
		MemoryMB:  int32(req.MemoryMB),  //nolint:gosec // bounded by validateRegister
		Metadata:  metadata,
	})
	if err != nil {
		s.internalError(w, r, "register worker", err)
		return
	}

	s.logger.InfoContext(r.Context(), "worker registered", "worker", worker.Name, "worker_id", worker.ID)
	writeJSON(w, http.StatusOK, workerapi.RegisterResponse{ID: worker.ID, Name: worker.Name})
}

func (s *server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "worker id must be a UUID")
		return
	}
	var req workerapi.HeartbeatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateHeartbeat(req); err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	cpu := req.CPUUsedPercent
	mem := int32(req.MemoryUsedMB) //nolint:gosec // bounded by validateHeartbeat
	n, err := s.workers.RecordHeartbeat(r.Context(), db.RecordHeartbeatParams{
		ID:             id,
		CpuUsedPercent: &cpu,
		MemoryUsedMB:   &mem,
	})
	if err != nil {
		s.internalError(w, r, "record heartbeat", err)
		return
	}
	if n == 0 {
		writeProblem(w, r, http.StatusNotFound, "worker is not registered")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.workers.ListWorkers(r.Context())
	if err != nil {
		s.internalError(w, r, "list workers", err)
		return
	}

	views := make([]workerView, 0, len(rows))
	for _, row := range rows {
		wk := row.Worker
		status := workerOffline
		if row.DBNow.Sub(wk.LastHeartbeatAt) <= s.workerStaleAfter {
			status = workerOnline
		}
		views = append(views, workerView{
			ID:              wk.ID,
			Name:            wk.Name,
			Status:          status,
			Slots:           wk.Slots,
			CPUMillis:       wk.CpuMillis,
			MemoryMB:        wk.MemoryMB,
			CPUUsedPercent:  wk.CpuUsedPercent,
			MemoryUsedMB:    wk.MemoryUsedMB,
			Metadata:        wk.Metadata,
			RegisteredAt:    wk.RegisteredAt,
			LastHeartbeatAt: wk.LastHeartbeatAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": views})
}

func validateRegister(req workerapi.RegisterRequest) error {
	switch {
	case req.Name == "" || len(req.Name) > maxWorkerNameLen || !workerNamePattern.MatchString(req.Name):
		return fmt.Errorf("name must be 1-%d characters of letters, digits, '.', '_' or '-', starting with a letter or digit", maxWorkerNameLen)
	case req.Slots < 1 || req.Slots > maxWorkerSlots:
		return fmt.Errorf("slots must be between 1 and %d", maxWorkerSlots)
	case req.CPUMillis < 1 || req.CPUMillis > math.MaxInt32:
		return errors.New("cpu_millis must be positive")
	case req.MemoryMB < 1 || req.MemoryMB > math.MaxInt32:
		return errors.New("memory_mb must be positive")
	}
	return nil
}

func validateHeartbeat(req workerapi.HeartbeatRequest) error {
	switch {
	case math.IsNaN(req.CPUUsedPercent) || req.CPUUsedPercent < 0 || req.CPUUsedPercent > 100:
		return errors.New("cpu_used_percent must be between 0 and 100")
	case req.MemoryUsedMB < 0 || req.MemoryUsedMB > math.MaxInt32:
		return errors.New("memory_used_mb must not be negative")
	}
	return nil
}

// decodeJSON reads a single JSON object from the request body. On failure it
// writes a 400 response and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		writeProblem(w, r, http.StatusBadRequest, invalidWorkerBody)
		return false
	}
	if dec.More() {
		writeProblem(w, r, http.StatusBadRequest, invalidWorkerBody)
		return false
	}
	return true
}

func (s *server) internalError(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.logger.ErrorContext(r.Context(), op+" failed", "err", err)
	writeProblem(w, r, http.StatusInternalServerError, "")
}

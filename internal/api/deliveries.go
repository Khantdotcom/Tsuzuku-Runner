package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
)

type deliveryView struct {
	ID             uuid.UUID `json:"id"`
	Kind           string    `json:"kind"`
	Status         string    `json:"status"`
	IdempotencyKey string    `json:"idempotency_key"`
	Target         string    `json:"target"`
	ExternalRef    *string   `json:"external_ref"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (s *server) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	if s.deliveries == nil {
		writeJSON(w, http.StatusOK, map[string]any{"deliveries": []deliveryView{}})
		return
	}
	rows, err := s.deliveries.List(r.Context(), j.ID)
	if err != nil {
		s.internalError(w, r, "list deliveries", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": deliveryViews(rows)})
}

func (s *server) handleRetryDeliveries(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	if s.deliveries == nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "delivery service is not configured")
		return
	}
	rows, err := s.deliveries.Retry(r.Context(), j.ID)
	switch {
	case errors.Is(err, job.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "job not found")
		return
	case errors.Is(err, job.ErrConflict):
		writeProblem(w, r, http.StatusConflict, err.Error())
		return
	case err != nil:
		s.internalError(w, r, "retry deliveries", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": deliveryViews(rows)})
}

func deliveryViews(rows []db.Delivery) []deliveryView {
	out := make([]deliveryView, 0, len(rows))
	for _, d := range rows {
		out = append(out, deliveryView{
			ID: d.ID, Kind: d.Kind, Status: d.Status, IdempotencyKey: d.IdempotencyKey,
			Target: d.Target, ExternalRef: d.ExternalRef, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		})
	}
	return out
}

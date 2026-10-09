package api

import (
	"context"
	"net/http"
	"time"
)

const readyTimeout = 2 * time.Second

// Pinger reports whether the database is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

func (s *server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()

	if err := s.db.Ping(ctx); err != nil {
		s.logger.WarnContext(r.Context(), "readiness check failed", "err", err)
		writeProblem(w, r, http.StatusServiceUnavailable, "database is unreachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

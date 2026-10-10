package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

// claimRecheckInterval bounds how long a waiting claim goes without looking
// at the database. Assignments made by this process wake it sooner.
const claimRecheckInterval = 2 * time.Second

// Claimer starts assigned jobs for workers. *job.Service implements it.
type Claimer interface {
	Claim(ctx context.Context, workerID uuid.UUID) (job.Claimed, bool, error)
}

// AssignmentSignal wakes waiting claims when jobs are scheduled.
// *scheduler.Notifier implements it.
type AssignmentSignal interface {
	Wait() <-chan struct{}
}

func (s *server) handleClaim(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "worker id must be a UUID")
		return
	}
	wait, ok := parseWait(w, r)
	if !ok {
		return
	}
	exists, err := s.workers.WorkerExists(r.Context(), id)
	if err != nil {
		s.internalError(w, r, "look up worker", err)
		return
	}
	if !exists {
		writeProblem(w, r, http.StatusNotFound, "worker is not registered")
		return
	}

	ctx := r.Context()
	deadline := time.Now().Add(wait)
	for {
		var wake <-chan struct{}
		if s.assignments != nil {
			wake = s.assignments.Wait()
		}

		c, found, err := s.claimer.Claim(ctx, id)
		if err != nil {
			if ctx.Err() == nil {
				s.internalError(w, r, "claim job", err)
			}
			return
		}
		if found {
			s.logger.InfoContext(ctx, "job claimed",
				"job_id", c.Job.ID, "job_number", c.Job.Number, "attempt", c.Attempt.AttemptNumber, "worker_id", id)
			writeJSON(w, http.StatusOK, workerapi.ClaimResponse{
				JobID:         c.Job.ID,
				JobNumber:     c.Job.Number,
				AttemptID:     c.Attempt.ID,
				AttemptNumber: int(c.Attempt.AttemptNumber),
				Spec:          c.Spec,
			})
			return
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		timer := time.NewTimer(min(remaining, claimRecheckInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.stopping:
			timer.Stop()
			w.WriteHeader(http.StatusNoContent)
			return
		case <-wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func parseWait(w http.ResponseWriter, r *http.Request) (time.Duration, bool) {
	v := r.URL.Query().Get("wait")
	if v == "" {
		return 0, true
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 || d > workerapi.MaxClaimWait {
		writeProblem(w, r, http.StatusBadRequest, "wait must be a duration from 0s to "+workerapi.MaxClaimWait.String()+", for example 25s")
		return 0, false
	}
	return d, true
}

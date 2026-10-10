package job

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
)

// EventCancelRequested is written when a running job is first asked to stop.
const EventCancelRequested = "job.cancel_requested"

// ErrJobFinished means the job already reached a final state.
var ErrJobFinished = errors.New("job has already finished")

// cancelAttempts bounds retries when the job changes state during a cancel.
const cancelAttempts = 3

// Cancel stops a job. A job that has not started yet is cancelled at once and
// immediate is true. A running job is marked for cancellation instead: its
// worker learns of it on its next heartbeat, stops the attempt, and the job
// ends CANCELLED when the worker reports. Cancelling a finished job returns
// ErrJobFinished.
func (s *Service) Cancel(ctx context.Context, jobID uuid.UUID) (j db.Job, immediate bool, err error) {
	for range cancelAttempts {
		j, immediate, err = s.cancelOnce(ctx, jobID)
		if !errors.Is(err, ErrConflict) {
			break
		}
	}
	return j, immediate, err
}

func (s *Service) cancelOnce(ctx context.Context, jobID uuid.UUID) (j db.Job, immediate bool, err error) {
	err = store.WithTx(ctx, s.pool, func(q *db.Queries) error {
		cur, err := q.GetJob(ctx, jobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load job: %w", err)
		}
		state := State(cur.State)
		switch {
		case state.Terminal():
			return fmt.Errorf("%w: job %d is %s", ErrJobFinished, cur.Number, state)
		case state == Queued || state == Scheduled:
			j, err = Transition(ctx, q, Change{
				JobID: jobID, From: state, To: Cancelled, Actor: ActorAPI, Reason: "cancelled on request",
			})
			immediate = err == nil
			return err
		}

		j, err = q.RequestJobCancel(ctx, jobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: job %d is no longer running", ErrConflict, cur.Number)
		}
		if err != nil {
			return fmt.Errorf("request cancellation: %w", err)
		}
		if cur.CancelRequestedAt != nil {
			return nil
		}
		if _, err := q.CreateJobEvent(ctx, db.CreateJobEventParams{
			JobID: jobID, Type: EventCancelRequested, Payload: []byte(`{"actor":"api"}`),
		}); err != nil {
			return fmt.Errorf("record cancellation event: %w", err)
		}
		return nil
	})
	return j, immediate, err
}

// CancelRequested returns the running attempts of workerID whose jobs were
// asked to stop.
func (s *Service) CancelRequested(ctx context.Context, workerID uuid.UUID) ([]uuid.UUID, error) {
	return s.q.ListCancelRequestedAttempts(ctx, workerID)
}

package job

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
)

const (
	reasonAbandonedScheduled = "assigned worker went offline before claim"
	reasonLeaseExpired       = "lease expired; worker stopped renewing"
	reasonRetryDue           = "retry backoff elapsed"
)

// PromoteRetrying moves a due RETRYING job back to QUEUED and clears its
// scheduled_at so the placer can pick it up on this or the next round.
func PromoteRetrying(ctx context.Context, q *db.Queries, jobID uuid.UUID) error {
	_, err := Transition(ctx, q, Change{
		JobID:          jobID,
		From:           Retrying,
		To:             Queued,
		Actor:          ActorScheduler,
		Reason:         reasonRetryDue,
		ClearWorker:    true,
		ClearCancel:    true,
		SetScheduledAt: true,
		Details:        map[string]any{"cause": "retry_due"},
	})
	return err
}

// RequeueAbandonedScheduled moves a SCHEDULED job whose worker went offline
// back to QUEUED and clears the assignment so another worker can take it.
func RequeueAbandonedScheduled(ctx context.Context, q *db.Queries, jobID uuid.UUID) error {
	_, err := Transition(ctx, q, Change{
		JobID:       jobID,
		From:        Scheduled,
		To:          Queued,
		Actor:       ActorScheduler,
		Reason:      reasonAbandonedScheduled,
		ClearWorker: true,
		Details:     map[string]any{"cause": "abandoned_scheduled"},
	})
	return err
}

// FailExpiredLease ends a mid-run attempt as an infrastructure failure,
// releases its lease, and requeues the job so it can be placed again.
func FailExpiredLease(ctx context.Context, q *db.Queries, jobID, attemptID uuid.UUID, from State) error {
	a, err := q.LockAttempt(ctx, attemptID)
	if err != nil {
		return fmt.Errorf("lock attempt: %w", err)
	}
	if a.JobID != jobID {
		return fmt.Errorf("attempt %s does not belong to job %s", attemptID, jobID)
	}

	if a.Status == AttemptRunning {
		errText := reasonLeaseExpired
		if _, err := q.FinishAttempt(ctx, db.FinishAttemptParams{
			ID: a.ID, Status: AttemptFailed, Error: &errText,
		}); err != nil {
			return fmt.Errorf("finish attempt: %w", err)
		}
		details, err := json.Marshal(map[string]any{
			"cause":     "lease_expired",
			"worker_id": a.WorkerID,
		})
		if err != nil {
			return err
		}
		failID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if err := q.CreateFailure(ctx, db.CreateFailureParams{
			ID:        failID,
			JobID:     jobID,
			AttemptID: &a.ID,
			Category:  CategoryInfrastructure,
			Message:   reasonLeaseExpired,
			Details:   details,
		}); err != nil {
			return fmt.Errorf("record failure: %w", err)
		}
	}

	if _, err := q.ReleaseLeaseByAttempt(ctx, attemptID); err != nil {
		return fmt.Errorf("release lease: %w", err)
	}

	_, err = Transition(ctx, q, Change{
		JobID:       jobID,
		From:        from,
		To:          Queued,
		Actor:       ActorScheduler,
		Reason:      reasonLeaseExpired,
		ClearWorker: true,
		ClearCancel: true,
		AttemptID:   &attemptID,
		Details:     map[string]any{"cause": "lease_expired", "worker_id": a.WorkerID},
	})
	return err
}

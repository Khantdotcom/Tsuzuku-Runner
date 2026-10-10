package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

// Attempt statuses, matching job_attempts.status.
const (
	AttemptRunning   = "RUNNING"
	AttemptSucceeded = "SUCCEEDED"
	AttemptFailed    = "FAILED"
	AttemptCancelled = "CANCELLED"
)

// EventRuntimeFinished is the timeline event written for every container an
// attempt used.
const EventRuntimeFinished = "attempt.runtime_finished"

// maxReasonLen keeps worker-supplied error text from bloating the history.
const maxReasonLen = 2000

var (
	// ErrAttemptNotFound means the attempt does not exist or belongs to
	// another worker.
	ErrAttemptNotFound = errors.New("attempt not found")
	// ErrAttemptFinished means the attempt already ended, so the report is
	// stale.
	ErrAttemptFinished = errors.New("attempt has already finished")
)

// Executing records a successful checkout and moves the job from PREPARING to
// EXECUTING.
func (s *Service) Executing(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.ExecutingRequest) error {
	return store.WithTx(ctx, s.pool, func(q *db.Queries) error {
		a, err := lockAttempt(ctx, q, workerID, attemptID)
		if err != nil {
			return err
		}
		if err := recordRuntime(ctx, q, a, req.Runtime, map[string]any{"commit": req.Commit}); err != nil {
			return err
		}
		_, err = Transition(ctx, q, Change{
			JobID:     a.JobID,
			From:      Preparing,
			To:        Executing,
			Actor:     ActorWorker,
			Reason:    "checked out " + shortCommit(req.Commit),
			AttemptID: &a.ID,
			Details:   map[string]any{"commit": req.Commit},
		})
		return err
	})
}

// Verifying records how the workload command ended. A command that timed out
// fails the job straight away and verify is false; otherwise the job moves to
// VERIFYING and verify is true.
func (s *Service) Verifying(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.VerifyingRequest) (verify bool, err error) {
	err = store.WithTx(ctx, s.pool, func(q *db.Queries) error {
		a, err := lockAttempt(ctx, q, workerID, attemptID)
		if err != nil {
			return err
		}
		exec := req.Execution
		if err := recordRuntime(ctx, q, a, req.Runtime, map[string]any{
			"exit_code": exec.ExitCode, "timed_out": exec.TimedOut,
		}); err != nil {
			return err
		}
		code := int32(exec.ExitCode) //nolint:gosec // exit codes are small
		if err := q.RecordAttemptExit(ctx, db.RecordAttemptExitParams{ID: a.ID, ExitCode: &code}); err != nil {
			return fmt.Errorf("record exit code: %w", err)
		}

		if exec.TimedOut {
			reason := fmt.Sprintf("timed out after %s", durationText(exec.DurationMS))
			return endAttempt(ctx, q, a, Executing, Failed, AttemptFailed, reason)
		}
		_, err = Transition(ctx, q, Change{
			JobID:     a.JobID,
			From:      Executing,
			To:        Verifying,
			Actor:     ActorWorker,
			Reason:    fmt.Sprintf("command exited with code %d", exec.ExitCode),
			AttemptID: &a.ID,
			Details:   map[string]any{"exit_code": exec.ExitCode, "duration_ms": exec.DurationMS},
		})
		verify = err == nil
		return err
	})
	return verify, err
}

// Finish ends an attempt and moves its job to a final state. A cancelled or
// errored attempt ends the job from whatever state it reached; otherwise the
// job must be VERIFYING and the outcome follows from the recorded results.
func (s *Service) Finish(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.FinishRequest) error {
	return store.WithTx(ctx, s.pool, func(q *db.Queries) error {
		a, err := lockAttempt(ctx, q, workerID, attemptID)
		if err != nil {
			return err
		}
		if req.Runtime != nil {
			if err := recordRuntime(ctx, q, a, *req.Runtime, nil); err != nil {
				return err
			}
		}
		j, err := q.GetJob(ctx, a.JobID)
		if err != nil {
			return fmt.Errorf("load job: %w", err)
		}
		from := State(j.State)

		switch {
		case req.Cancelled:
			return endAttempt(ctx, q, a, from, Cancelled, AttemptCancelled, "cancelled while "+string(from))
		case req.Error != "":
			return endAttempt(ctx, q, a, from, Failed, AttemptFailed, truncate(req.Error, maxReasonLen))
		case from != Verifying:
			return fmt.Errorf("%w: job %d is %s, not %s", ErrConflict, j.Number, from, Verifying)
		}
		to, status, reason := decide(a.ExitCode)
		return endAttempt(ctx, q, a, from, to, status, reason)
	})
}

// decide turns the recorded results of an attempt into its outcome.
func decide(exitCode *int32) (to State, attemptStatus, reason string) {
	switch {
	case exitCode == nil:
		return Failed, AttemptFailed, "no exit code was recorded"
	case *exitCode != 0:
		return Failed, AttemptFailed, fmt.Sprintf("command exited with code %d", *exitCode)
	default:
		return Completed, AttemptSucceeded, "command succeeded"
	}
}

// endAttempt moves the job to a final state and closes the attempt with the
// matching status, in the caller's transaction.
func endAttempt(ctx context.Context, q *db.Queries, a db.JobAttempt, from, to State, status, reason string) error {
	if _, err := Transition(ctx, q, Change{
		JobID: a.JobID, From: from, To: to, Actor: ActorWorker, Reason: reason, AttemptID: &a.ID,
	}); err != nil {
		return err
	}
	var errText *string
	if status != AttemptSucceeded {
		errText = &reason
	}
	if _, err := q.FinishAttempt(ctx, db.FinishAttemptParams{ID: a.ID, Status: status, Error: errText}); err != nil {
		return fmt.Errorf("finish attempt: %w", err)
	}
	return nil
}

// lockAttempt locks an attempt for the rest of the transaction and checks
// that workerID owns it and that it is still running. Another worker's
// attempt is reported as not found, so IDs cannot be probed.
func lockAttempt(ctx context.Context, q *db.Queries, workerID, attemptID uuid.UUID) (db.JobAttempt, error) {
	a, err := q.LockAttempt(ctx, attemptID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && a.WorkerID != workerID) {
		return db.JobAttempt{}, ErrAttemptNotFound
	}
	if err != nil {
		return db.JobAttempt{}, fmt.Errorf("lock attempt: %w", err)
	}
	if a.Status != AttemptRunning {
		return db.JobAttempt{}, fmt.Errorf("%w: attempt %d is %s", ErrAttemptFinished, a.AttemptNumber, a.Status)
	}
	return a, nil
}

// recordRuntime stores a container the attempt used and adds it to the
// job's timeline.
func recordRuntime(ctx context.Context, q *db.Queries, a db.JobAttempt, rt workerapi.Runtime, details map[string]any) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	finished := rt.FinishedAt
	if _, err := q.CreateRuntime(ctx, db.CreateRuntimeParams{
		ID:             id,
		AttemptID:      a.ID,
		Role:           rt.Role,
		Image:          rt.Image,
		ContainerID:    nonEmpty(rt.ContainerID),
		VolumeName:     nonEmpty(rt.VolumeName),
		CpuMillis:      int32(rt.CPUMillis), //nolint:gosec // validated by the API
		MemoryMB:       int32(rt.MemoryMB),  //nolint:gosec // validated by the API
		NetworkEnabled: rt.Network,
		CreatedAt:      rt.StartedAt,
		DestroyedAt:    &finished,
	}); err != nil {
		return fmt.Errorf("record %s runtime: %w", rt.Role, err)
	}

	fields := make(map[string]any, len(details)+4)
	maps.Copy(fields, details)
	fields["role"], fields["image"] = rt.Role, rt.Image
	fields["container_id"], fields["duration_ms"] = rt.ContainerID, rt.FinishedAt.Sub(rt.StartedAt).Milliseconds()
	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if _, err := q.CreateJobEvent(ctx, db.CreateJobEventParams{
		JobID: a.JobID, AttemptID: &a.ID, Type: EventRuntimeFinished, Payload: payload,
	}); err != nil {
		return fmt.Errorf("record runtime event: %w", err)
	}
	return nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// truncate shortens s to at most n bytes without splitting a UTF-8 character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

func durationText(ms int64) string {
	if ms%1000 == 0 {
		return fmt.Sprintf("%ds", ms/1000)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

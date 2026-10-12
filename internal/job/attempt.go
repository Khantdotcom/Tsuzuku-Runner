package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
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

// Verifying records how the workload command ended. If the job was asked to
// stop, or the command timed out, the attempt ends here and verify is false;
// otherwise the job moves to VERIFYING and verify is true.
func (s *Service) Verifying(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.VerifyingRequest) (verify bool, err error) {
	var ended *db.JobAttempt
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
		j, err := q.GetJob(ctx, a.JobID)
		if err != nil {
			return fmt.Errorf("load job: %w", err)
		}

		switch {
		case j.CancelRequestedAt != nil:
			ended = &a
			return s.endAttempt(ctx, q, a, Executing, cancelledOnRequest())
		case exec.TimedOut:
			ended = &a
			o := failed("timed out after "+durationText(exec.DurationMS), CategoryTimeout)
			o.details = map[string]any{"duration_ms": exec.DurationMS}
			return s.endAttempt(ctx, q, a, Executing, o)
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
	if err == nil && ended != nil {
		s.archiveEvidence(ctx, *ended)
	}
	return verify, err
}

// Finish ends an attempt and moves its job to a final state. A cancelled or
// errored attempt ends the job from whatever state it reached; otherwise the
// job must be VERIFYING and the outcome follows from the recorded results.
// A job that was asked to stop always ends CANCELLED.
func (s *Service) Finish(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.FinishRequest) error {
	var ended db.JobAttempt
	err := store.WithTx(ctx, s.pool, func(q *db.Queries) error {
		a, err := lockAttempt(ctx, q, workerID, attemptID)
		if err != nil {
			return err
		}
		ended = a
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
		case j.CancelRequestedAt != nil:
			return s.endAttempt(ctx, q, a, from, cancelledOnRequest())
		case req.Cancelled:
			return s.endAttempt(ctx, q, a, from, outcome{
				to: Cancelled, status: AttemptCancelled, reason: "cancelled while " + string(from),
			})
		case req.Error != "":
			o := failed(truncate(req.Error, maxReasonLen), stageCategory(req.Stage))
			if req.Stage != "" {
				o.details = map[string]any{"stage": req.Stage}
			}
			return s.endAttempt(ctx, q, a, from, o)
		case from != Verifying:
			return fmt.Errorf("%w: job %d is %s, not %s", ErrConflict, j.Number, from, Verifying)
		}

		w, err := q.GetWorkload(ctx, j.WorkloadID)
		if err != nil {
			return fmt.Errorf("load workload: %w", err)
		}
		var spec workload.Spec
		if err := json.Unmarshal(w.Spec, &spec); err != nil {
			return fmt.Errorf("decode workload spec: %w", err)
		}
		results := finishVerifications(req)
		for _, r := range results {
			if err := recordRuntime(ctx, q, a, r.Runtime, map[string]any{
				"exit_code": r.Result.ExitCode, "timed_out": r.Result.TimedOut, "check": r.Name,
			}); err != nil {
				return err
			}
		}
		v := judge(a.ExitCode, spec, results, req.Integrity)
		if err := recordVerification(ctx, q, a, v); err != nil {
			return err
		}
		return s.endAttempt(ctx, q, a, from, v.outcome)
	})
	if err == nil {
		if req.Integrity != nil && req.Integrity.Diff != "" {
			s.storeDiffArtifact(ctx, ended, req.Integrity.Diff)
		}
		s.archiveEvidence(ctx, ended)
		s.maybeDeliver(ctx, ended.JobID)
	}
	return err
}

// maybeDeliver runs outbound adapters after a successful COMPLETED finish.
func (s *Service) maybeDeliver(ctx context.Context, jobID uuid.UUID) {
	if s.deliverer == nil {
		return
	}
	j, err := s.q.GetJob(ctx, jobID)
	if err != nil || j.State != string(Completed) {
		return
	}
	s.deliverer.Deliver(ctx, jobID)
}

func cancelledOnRequest() outcome {
	return outcome{to: Cancelled, status: AttemptCancelled, reason: "cancelled on request"}
}

// finishVerifications merges legacy Verification with Verifications.
func finishVerifications(req workerapi.FinishRequest) []workerapi.VerificationResult {
	if len(req.Verifications) > 0 {
		out := make([]workerapi.VerificationResult, len(req.Verifications))
		copy(out, req.Verifications)
		for i := range out {
			if out[i].Name == "" {
				out[i].Name = "verification"
			}
		}
		return out
	}
	if req.Verification == nil {
		return nil
	}
	v := *req.Verification
	if v.Name == "" {
		v.Name = "verification"
	}
	return []workerapi.VerificationResult{v}
}

// endAttempt applies the retry/block policy, moves the job, closes the
// attempt, and records the failure if there is one, in the caller's
// transaction. The attempt always finishes with o.status; only the job state
// may become RETRYING or BLOCKED instead of FAILED.
func (s *Service) endAttempt(ctx context.Context, q *db.Queries, a db.JobAttempt, from State, o outcome) error {
	o, when := applyPolicy(o, int(a.AttemptNumber), s.maxAttempts, s.retryBackoff, s.retryBackoffCap)
	eventDetails := maps.Clone(o.details)
	if eventDetails == nil {
		eventDetails = map[string]any{}
	}
	change := Change{
		JobID: a.JobID, From: from, To: o.to, Actor: ActorWorker, Reason: o.reason, AttemptID: &a.ID,
		Details: eventDetails,
	}
	if o.to == Retrying {
		change.ClearWorker = true
		change.ClearCancel = true
		change.SetScheduledAt = true
		change.ScheduledAt = when
		change.Details["attempt"] = a.AttemptNumber
		change.Details["max_attempts"] = s.maxAttempts
		if when != nil {
			change.Details["retry_at"] = when.UTC().Format(time.RFC3339Nano)
		}
	}
	if o.to == Blocked {
		change.ClearWorker = true
		change.ClearCancel = true
	}
	if _, err := Transition(ctx, q, change); err != nil {
		return err
	}
	var errText *string
	if o.status != AttemptSucceeded {
		errText = &o.reason
	}
	if _, err := q.FinishAttempt(ctx, db.FinishAttemptParams{ID: a.ID, Status: o.status, Error: errText}); err != nil {
		return fmt.Errorf("finish attempt: %w", err)
	}
	if _, err := q.ReleaseLeaseByAttempt(ctx, a.ID); err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	if o.category == "" {
		return nil
	}
	details, err := json.Marshal(o.details)
	if err != nil {
		return err
	}
	if o.details == nil {
		details = []byte("{}")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if err := q.CreateFailure(ctx, db.CreateFailureParams{
		ID: id, JobID: a.JobID, AttemptID: &a.ID, Category: o.category, Message: o.reason, Details: details,
	}); err != nil {
		return fmt.Errorf("record failure: %w", err)
	}
	return nil
}

// applyPolicy may turn a FAILED outcome into RETRYING (with backoff) or
// BLOCKED. Successful and cancelled outcomes are unchanged.
func applyPolicy(o outcome, attemptNumber, maxAttempts int, base, backoffCap time.Duration) (outcome, *time.Time) {
	if o.to != Failed || o.category == "" {
		return o, nil
	}
	switch ActionFor(o.category) {
	case ActionRetry:
		if attemptNumber < maxAttempts {
			when := time.Now().Add(RetryBackoff(attemptNumber, base, backoffCap))
			o.to = Retrying
			return o, &when
		}
		return o, nil
	case ActionBlock:
		o.to = Blocked
		return o, nil
	default:
		return o, nil
	}
}

// recordVerification stores a verification run and its checks.
func recordVerification(ctx context.Context, q *db.Queries, a db.JobAttempt, v verdict) error {
	runID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	now := time.Now()
	started := now
	for _, c := range v.checks {
		if c.startedAt != nil && c.startedAt.Before(started) {
			started = *c.startedAt
		}
	}
	if _, err := q.CreateVerificationRun(ctx, db.CreateVerificationRunParams{
		ID: runID, JobID: a.JobID, AttemptID: a.ID, Status: v.runStatus, StartedAt: started, FinishedAt: &now,
	}); err != nil {
		return fmt.Errorf("record verification run: %w", err)
	}
	for _, c := range v.checks {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if err := q.CreateVerificationCheck(ctx, db.CreateVerificationCheckParams{
			ID: id, VerificationRunID: runID, Name: c.name, Kind: c.kind, Command: c.command,
			Status: c.status, ExitCode: c.exitCode, Output: c.output,
			StartedAt: c.startedAt, FinishedAt: c.finishedAt,
		}); err != nil {
			return fmt.Errorf("record %s check: %w", c.name, err)
		}
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

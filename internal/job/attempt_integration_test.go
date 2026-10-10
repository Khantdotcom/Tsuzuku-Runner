//go:build integration

package job_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/storetest"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

const commit = "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d"

// claimedAttempt submits a job, assigns it to a new worker, and claims it, so
// the job is PREPARING with a RUNNING attempt.
func claimedAttempt(t *testing.T, pool *pgxpool.Pool) (svc *job.Service, workerID uuid.UUID, c job.Claimed) {
	t.Helper()
	svc = job.NewService(pool)
	workerID = addWorker(t, pool, "runner-"+uuid.NewString()[:8])
	submitAssigned(t, pool, "go test ./...", workerID)
	c, found, err := svc.Claim(t.Context(), workerID)
	if err != nil || !found {
		t.Fatalf("Claim: found %v, err %v", found, err)
	}
	return svc, workerID, c
}

func runtimeFor(role string) workerapi.Runtime {
	start := time.Now().Add(-5 * time.Second).UTC()
	return workerapi.Runtime{
		Role: role, Image: "golang:1.27", ContainerID: "c-" + role, VolumeName: "tsuzuku-v",
		CPUMillis: 1000, MemoryMB: 512, StartedAt: start, FinishedAt: start.Add(2 * time.Second),
	}
}

func executed(exitCode int, timedOut bool) workerapi.VerifyingRequest {
	return workerapi.VerifyingRequest{
		Execution: workerapi.StepResult{ExitCode: exitCode, TimedOut: timedOut, DurationMS: 2000},
		Runtime:   runtimeFor(workerapi.RoleExecute),
	}
}

func jobState(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) db.Job {
	t.Helper()
	j, err := db.New(pool).GetJob(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func attemptRow(t *testing.T, pool *pgxpool.Pool, jobID uuid.UUID) db.JobAttempt {
	t.Helper()
	attempts, err := db.New(pool).ListAttempts(t.Context(), jobID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %v, err %v", attempts, err)
	}
	return attempts[0]
}

func lastReason(t *testing.T, pool *pgxpool.Pool, jobID uuid.UUID) string {
	t.Helper()
	history, err := db.New(pool).ListTransitions(t.Context(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	return history[len(history)-1].Reason
}

func TestAttemptSucceeds(t *testing.T) {
	pool := storetest.NewPool(t)
	svc, w, c := claimedAttempt(t, pool)
	ctx := t.Context()

	if err := svc.Executing(ctx, w, c.Attempt.ID, workerapi.ExecutingRequest{Commit: commit, Runtime: runtimeFor(workerapi.RolePrepare)}); err != nil {
		t.Fatalf("Executing: %v", err)
	}
	if j := jobState(t, pool, c.Job.ID); j.State != string(job.Executing) {
		t.Fatalf("state after executing = %s", j.State)
	}
	if got := lastReason(t, pool, c.Job.ID); got != "checked out 7fd1a60b01f9" {
		t.Errorf("reason = %q", got)
	}

	verify, err := svc.Verifying(ctx, w, c.Attempt.ID, executed(0, false))
	if err != nil || !verify {
		t.Fatalf("Verifying: verify %v, err %v", verify, err)
	}
	if j := jobState(t, pool, c.Job.ID); j.State != string(job.Verifying) {
		t.Fatalf("state after verifying = %s", j.State)
	}

	if err := svc.Finish(ctx, w, c.Attempt.ID, workerapi.FinishRequest{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	j := jobState(t, pool, c.Job.ID)
	if j.State != string(job.Completed) || j.FinishedAt == nil {
		t.Errorf("job = %s finished %v, want COMPLETED with finished_at", j.State, j.FinishedAt)
	}
	a := attemptRow(t, pool, c.Job.ID)
	if a.Status != job.AttemptSucceeded || a.ExitCode == nil || *a.ExitCode != 0 || a.Error != nil || a.FinishedAt == nil {
		t.Errorf("attempt = %+v", a)
	}

	runtimes, err := db.New(pool).ListRuntimes(ctx, c.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimes) != 2 || runtimes[0].Role != workerapi.RolePrepare || runtimes[1].Role != workerapi.RoleExecute {
		t.Fatalf("runtimes = %+v", runtimes)
	}
	if r := runtimes[1]; r.Kind != "docker" || *r.ContainerID != "c-execute" || r.CpuMillis != 1000 || r.DestroyedAt == nil {
		t.Errorf("execute runtime = %+v", r)
	}
	if got := count(t, pool, "SELECT count(*) FROM job_events WHERE job_id = $1 AND type = $2", c.Job.ID, job.EventRuntimeFinished); got != 2 {
		t.Errorf("runtime events = %d, want 2", got)
	}
}

func TestAttemptNonZeroExitFails(t *testing.T) {
	pool := storetest.NewPool(t)
	svc, w, c := claimedAttempt(t, pool)
	ctx := t.Context()

	if err := svc.Executing(ctx, w, c.Attempt.ID, workerapi.ExecutingRequest{Commit: commit, Runtime: runtimeFor(workerapi.RolePrepare)}); err != nil {
		t.Fatal(err)
	}
	if verify, err := svc.Verifying(ctx, w, c.Attempt.ID, executed(2, false)); err != nil || !verify {
		t.Fatalf("Verifying: verify %v, err %v", verify, err)
	}
	if err := svc.Finish(ctx, w, c.Attempt.ID, workerapi.FinishRequest{}); err != nil {
		t.Fatal(err)
	}
	if j := jobState(t, pool, c.Job.ID); j.State != string(job.Failed) {
		t.Errorf("state = %s, want FAILED", j.State)
	}
	a := attemptRow(t, pool, c.Job.ID)
	if a.Status != job.AttemptFailed || a.Error == nil || *a.Error != "command exited with code 2" {
		t.Errorf("attempt = %+v", a)
	}
}

func TestAttemptTimeoutFailsWithoutVerification(t *testing.T) {
	pool := storetest.NewPool(t)
	svc, w, c := claimedAttempt(t, pool)
	ctx := t.Context()

	if err := svc.Executing(ctx, w, c.Attempt.ID, workerapi.ExecutingRequest{Commit: commit, Runtime: runtimeFor(workerapi.RolePrepare)}); err != nil {
		t.Fatal(err)
	}
	verify, err := svc.Verifying(ctx, w, c.Attempt.ID, executed(137, true))
	if err != nil || verify {
		t.Fatalf("Verifying: verify %v, err %v; want the job ended without verification", verify, err)
	}
	if j := jobState(t, pool, c.Job.ID); j.State != string(job.Failed) {
		t.Errorf("state = %s, want FAILED", j.State)
	}
	if got := lastReason(t, pool, c.Job.ID); got != "timed out after 2s" {
		t.Errorf("reason = %q", got)
	}

	err = svc.Finish(ctx, w, c.Attempt.ID, workerapi.FinishRequest{})
	if !errors.Is(err, job.ErrAttemptFinished) {
		t.Errorf("Finish after timeout: err = %v, want ErrAttemptFinished", err)
	}
}

func TestAttemptErrorFromAnyRunningState(t *testing.T) {
	pool := storetest.NewPool(t)
	svc, w, c := claimedAttempt(t, pool)
	rt := runtimeFor(workerapi.RolePrepare)
	reason := "check out repository: checkout failed: git exited with code 128: " + strings.Repeat("é", 1500)

	if err := svc.Finish(t.Context(), w, c.Attempt.ID, workerapi.FinishRequest{Error: reason, Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	j := jobState(t, pool, c.Job.ID)
	if j.State != string(job.Failed) || j.FinishedAt == nil {
		t.Errorf("job = %s, want FAILED from PREPARING", j.State)
	}
	a := attemptRow(t, pool, c.Job.ID)
	if a.Status != job.AttemptFailed || a.Error == nil || !strings.HasPrefix(*a.Error, "check out repository") {
		t.Fatalf("attempt = %+v", a)
	}
	if len(*a.Error) > 2003 || !strings.HasSuffix(*a.Error, "...") {
		t.Errorf("error text was not truncated: %d bytes", len(*a.Error))
	}
	if got := count(t, pool, "SELECT count(*) FROM runtimes WHERE attempt_id = $1", c.Attempt.ID); got != 1 {
		t.Errorf("runtimes = %d, want the failed checkout container", got)
	}
}

func TestAttemptCancelled(t *testing.T) {
	pool := storetest.NewPool(t)
	svc, w, c := claimedAttempt(t, pool)
	ctx := t.Context()

	if err := svc.Executing(ctx, w, c.Attempt.ID, workerapi.ExecutingRequest{Commit: commit, Runtime: runtimeFor(workerapi.RolePrepare)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Finish(ctx, w, c.Attempt.ID, workerapi.FinishRequest{Cancelled: true}); err != nil {
		t.Fatal(err)
	}
	if j := jobState(t, pool, c.Job.ID); j.State != string(job.Cancelled) {
		t.Errorf("state = %s, want CANCELLED", j.State)
	}
	if a := attemptRow(t, pool, c.Job.ID); a.Status != job.AttemptCancelled {
		t.Errorf("attempt status = %s", a.Status)
	}
	if got := lastReason(t, pool, c.Job.ID); got != "cancelled while EXECUTING" {
		t.Errorf("reason = %q", got)
	}
}

func TestAttemptReportsAreFenced(t *testing.T) {
	pool := storetest.NewPool(t)
	svc, w, c := claimedAttempt(t, pool)
	ctx := t.Context()
	other := addWorker(t, pool, "intruder")
	execReq := workerapi.ExecutingRequest{Commit: commit, Runtime: runtimeFor(workerapi.RolePrepare)}

	if err := svc.Executing(ctx, other, c.Attempt.ID, execReq); !errors.Is(err, job.ErrAttemptNotFound) {
		t.Errorf("another worker's report: err = %v, want ErrAttemptNotFound", err)
	}
	if err := svc.Executing(ctx, w, uuid.New(), execReq); !errors.Is(err, job.ErrAttemptNotFound) {
		t.Errorf("unknown attempt: err = %v, want ErrAttemptNotFound", err)
	}
	if _, err := svc.Verifying(ctx, w, c.Attempt.ID, executed(0, false)); !errors.Is(err, job.ErrIllegalTransition) && !errors.Is(err, job.ErrConflict) {
		t.Errorf("verifying before executing: err = %v, want a state conflict", err)
	}
	if err := svc.Finish(ctx, w, c.Attempt.ID, workerapi.FinishRequest{}); !errors.Is(err, job.ErrConflict) {
		t.Errorf("deciding before verifying: err = %v, want ErrConflict", err)
	}
	if got := count(t, pool, "SELECT count(*) FROM runtimes WHERE attempt_id = $1", c.Attempt.ID); got != 0 {
		t.Errorf("rejected reports left %d runtime rows; they must roll back", got)
	}
	if j := jobState(t, pool, c.Job.ID); j.State != string(job.Preparing) {
		t.Errorf("state = %s, want PREPARING untouched", j.State)
	}

	if err := svc.Executing(ctx, w, c.Attempt.ID, execReq); err != nil {
		t.Fatal(err)
	}
	if err := svc.Executing(ctx, w, c.Attempt.ID, execReq); err == nil {
		t.Error("a repeated executing report was accepted")
	}
}

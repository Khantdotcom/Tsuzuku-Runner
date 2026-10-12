//go:build integration

package job_test

import (
	"testing"
	"time"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/storetest"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

func TestRetryableFailureEntersRetryingWithBackoff(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool, job.WithRetryPolicy(3, time.Second, 5*time.Minute))
	w, c := claimSpec(t, pool, svc, newSpec(t, "make"))

	before := time.Now()
	if err := svc.Finish(t.Context(), w, c.Attempt.ID, workerapi.FinishRequest{
		Error: "pull image: not found", Stage: workerapi.StageImage,
	}); err != nil {
		t.Fatal(err)
	}
	j := jobState(t, pool, c.Job.ID)
	if j.State != string(job.Retrying) {
		t.Fatalf("state = %s, want RETRYING", j.State)
	}
	if j.AssignedWorkerID != nil {
		t.Error("RETRYING job still has an assigned worker")
	}
	if j.ScheduledAt == nil || j.ScheduledAt.Before(before.Add(500*time.Millisecond)) {
		t.Fatalf("scheduled_at = %v, want about 1s after failure", j.ScheduledAt)
	}
	a := attemptRow(t, pool, c.Job.ID)
	if a.Status != job.AttemptFailed {
		t.Errorf("attempt status = %s, want FAILED", a.Status)
	}
}

func TestTestFailureDoesNotRetry(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	w, c := claimSpec(t, pool, svc, newSpec(t, "make"))
	ctx := t.Context()

	if err := svc.Executing(ctx, w, c.Attempt.ID, workerapi.ExecutingRequest{Commit: commit, Runtime: runtimeFor(workerapi.RolePrepare)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verifying(ctx, w, c.Attempt.ID, executed(1, false)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Finish(ctx, w, c.Attempt.ID, workerapi.FinishRequest{}); err != nil {
		t.Fatal(err)
	}
	j := jobState(t, pool, c.Job.ID)
	if j.State != string(job.Failed) || j.FinishedAt == nil {
		t.Fatalf("state = %s, want FAILED", j.State)
	}
}

func TestRetryExhaustionFails(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool, job.WithRetryPolicy(1, time.Millisecond, time.Second))
	w, c := claimSpec(t, pool, svc, newSpec(t, "make"))

	if err := svc.Finish(t.Context(), w, c.Attempt.ID, workerapi.FinishRequest{
		Error: "daemon down", Stage: workerapi.StageExecute,
	}); err != nil {
		t.Fatal(err)
	}
	j := jobState(t, pool, c.Job.ID)
	if j.State != string(job.Failed) || j.FinishedAt == nil {
		t.Fatalf("state = %s, want FAILED after last attempt", j.State)
	}
}

func TestCancelRetryingJob(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	w, c := claimSpec(t, pool, svc, newSpec(t, "make"))
	if err := svc.Finish(t.Context(), w, c.Attempt.ID, workerapi.FinishRequest{
		Error: "transient", Stage: workerapi.StageWorkspace,
	}); err != nil {
		t.Fatal(err)
	}
	if jobState(t, pool, c.Job.ID).State != string(job.Retrying) {
		t.Fatal("setup: want RETRYING")
	}

	j, immediate, err := svc.Cancel(t.Context(), c.Job.ID)
	if err != nil || !immediate || j.State != string(job.Cancelled) {
		t.Fatalf("Cancel: state %s immediate %v err %v", j.State, immediate, err)
	}
}

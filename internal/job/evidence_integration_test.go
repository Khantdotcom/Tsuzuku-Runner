//go:build integration

package job_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Khantdotcom/tsuzuku-runner/internal/artifact"
	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/storetest"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

// claimSpec submits spec, assigns it to a new worker, and claims it with svc.
func claimSpec(t *testing.T, pool *pgxpool.Pool, svc *job.Service, spec workload.Spec) (uuid.UUID, job.Claimed) {
	t.Helper()
	w := addWorker(t, pool, "runner-"+uuid.NewString()[:8])
	sub, err := svc.Submit(t.Context(), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transition(t, pool, job.Change{
		JobID: sub.Job.ID, From: job.Queued, To: job.Scheduled, Actor: job.ActorScheduler, AssignWorker: &w,
	}); err != nil {
		t.Fatal(err)
	}
	c, found, err := svc.Claim(t.Context(), w)
	if err != nil || !found {
		t.Fatalf("Claim: found %v, err %v", found, err)
	}
	return w, c
}

func newFS(t *testing.T) *artifact.FS {
	t.Helper()
	fs, err := artifact.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs
}

func chunk(seq int, stream, data string) workerapi.LogChunk {
	return workerapi.LogChunk{Seq: seq, Stream: stream, Data: []byte(data)}
}

// runToVerifying reports a checkout and a command that exited with exitCode.
func runToVerifying(t *testing.T, svc *job.Service, w uuid.UUID, c job.Claimed, exitCode int) {
	t.Helper()
	ctx := t.Context()
	if err := svc.Executing(ctx, w, c.Attempt.ID, workerapi.ExecutingRequest{Commit: commit, Runtime: runtimeFor(workerapi.RolePrepare)}); err != nil {
		t.Fatal(err)
	}
	if verify, err := svc.Verifying(ctx, w, c.Attempt.ID, executed(exitCode, false)); err != nil || !verify {
		t.Fatalf("Verifying: verify %v, err %v", verify, err)
	}
}

func TestAppendLogsIsIdempotentAndCapped(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool, job.WithMaxLogBytes(10))
	w, c := claimSpec(t, pool, svc, newSpec(t, "make"))
	ctx := t.Context()

	truncated, err := svc.AppendLogs(ctx, w, c.Attempt.ID, []workerapi.LogChunk{
		chunk(0, workerapi.StreamStdout, "hello"),
		chunk(1, workerapi.StreamStderr, "world!!"),
	})
	if err != nil || !truncated {
		t.Fatalf("AppendLogs: truncated %v, err %v; want the 10-byte limit reached", truncated, err)
	}
	// A resent batch and anything past the limit store nothing.
	truncated, err = svc.AppendLogs(ctx, w, c.Attempt.ID, []workerapi.LogChunk{
		chunk(0, workerapi.StreamStdout, "hello"),
		chunk(2, workerapi.StreamStdout, "more"),
	})
	if err != nil || !truncated {
		t.Fatalf("second AppendLogs: truncated %v, err %v", truncated, err)
	}

	chunks, err := db.New(pool).ListAttemptLogChunks(ctx, c.Attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || string(chunks[0].Data) != "hello" || string(chunks[1].Data) != "world" ||
		chunks[1].Stream != workerapi.StreamStderr {
		t.Errorf("stored chunks = %+v", chunks)
	}
	if got := count(t, pool, "SELECT count(*) FROM job_events WHERE job_id = $1 AND type = $2", c.Job.ID, job.EventLogsTruncated); got != 1 {
		t.Errorf("truncation events = %d, want 1", got)
	}

	other := addWorker(t, pool, "intruder")
	if _, err := svc.AppendLogs(ctx, other, c.Attempt.ID, []workerapi.LogChunk{chunk(9, workerapi.StreamStdout, "x")}); !errors.Is(err, job.ErrAttemptNotFound) {
		t.Errorf("another worker's logs: err = %v, want ErrAttemptNotFound", err)
	}
}

func TestAppendLogsAfterFinishIsRejected(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	w, c := claimSpec(t, pool, svc, newSpec(t, "make"))
	if err := svc.Finish(t.Context(), w, c.Attempt.ID, workerapi.FinishRequest{Cancelled: true}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.AppendLogs(t.Context(), w, c.Attempt.ID, []workerapi.LogChunk{chunk(0, workerapi.StreamStdout, "late")})
	if !errors.Is(err, job.ErrAttemptFinished) {
		t.Errorf("err = %v, want ErrAttemptFinished", err)
	}
}

func TestFinishStoresLogArtifacts(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool, job.WithArtifacts(newFS(t)))
	w, c := claimSpec(t, pool, svc, newSpec(t, "make"))
	ctx := t.Context()

	runToVerifying(t, svc, w, c, 0)
	if _, err := svc.AppendLogs(ctx, w, c.Attempt.ID, []workerapi.LogChunk{
		chunk(0, workerapi.StreamStdout, "line 1\n"),
		chunk(1, workerapi.StreamStderr, "warn\n"),
		chunk(2, workerapi.StreamStdout, "line 2\n"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Finish(ctx, w, c.Attempt.ID, workerapi.FinishRequest{}); err != nil {
		t.Fatal(err)
	}

	e, err := svc.Evidence(ctx, c.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{job.ArtifactStdout: "line 1\nline 2\n", job.ArtifactStderr: "warn\n"}
	if len(e.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v", e.Artifacts)
	}
	for _, a := range e.Artifacts {
		content := want[a.Name]
		sum := sha256.Sum256([]byte(content))
		if a.SizeBytes != int64(len(content)) || a.Sha256 != hex.EncodeToString(sum[:]) || a.AttemptID == nil || *a.AttemptID != c.Attempt.ID {
			t.Errorf("artifact %s = %+v", a.Name, a)
		}
		got, r, err := svc.OpenArtifact(ctx, c.Job.ID, a.ID)
		if err != nil {
			t.Fatalf("OpenArtifact(%s): %v", a.Name, err)
		}
		data, _ := io.ReadAll(r)
		_ = r.Close()
		if got.ID != a.ID || string(data) != content {
			t.Errorf("%s content = %q, want %q", a.Name, data, content)
		}
	}
	if _, _, err := svc.OpenArtifact(ctx, uuid.New(), e.Artifacts[0].ID); !errors.Is(err, job.ErrArtifactNotFound) {
		t.Errorf("artifact under another job: err = %v, want ErrArtifactNotFound", err)
	}

	if len(e.Verifications) != 1 || e.Verifications[0].Run.Status != job.CheckPassed || len(e.Verifications[0].Checks) != 1 {
		t.Errorf("verifications = %+v", e.Verifications)
	}
	if len(e.Failures) != 0 {
		t.Errorf("failures = %+v, want none for a passing job", e.Failures)
	}
}

func TestFinishWithFailedVerification(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	spec := newSpec(t, "make build")
	spec.Verification = &workload.Verification{Command: "make lint"}
	w, c := claimSpec(t, pool, svc, spec)
	ctx := t.Context()

	runToVerifying(t, svc, w, c, 0)
	err := svc.Finish(ctx, w, c.Attempt.ID, workerapi.FinishRequest{Verification: &workerapi.VerificationResult{
		Command:    "make lint",
		Result:     workerapi.StepResult{ExitCode: 1, DurationMS: 900},
		Runtime:    runtimeFor(workerapi.RoleVerify),
		OutputTail: "lint: 3 problems",
	}})
	if err != nil {
		t.Fatal(err)
	}

	if j := jobState(t, pool, c.Job.ID); j.State != string(job.Failed) {
		t.Errorf("state = %s, want FAILED", j.State)
	}
	if got := lastReason(t, pool, c.Job.ID); got != "verification exited with code 1" {
		t.Errorf("reason = %q", got)
	}
	e, err := svc.Evidence(ctx, c.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Verifications) != 1 || e.Verifications[0].Run.Status != job.CheckFailed {
		t.Fatalf("verifications = %+v", e.Verifications)
	}
	checks := e.Verifications[0].Checks
	if len(checks) != 2 || checks[0].Status != job.CheckPassed || checks[1].Status != job.CheckFailed ||
		checks[1].Output != "lint: 3 problems" || *checks[1].Command != "make lint" || *checks[1].ExitCode != 1 {
		t.Errorf("checks = %+v", checks)
	}
	if len(e.Failures) != 1 || e.Failures[0].Category != job.CategoryTest || e.Failures[0].Message != "verification exited with code 1" {
		t.Errorf("failures = %+v", e.Failures)
	}
	if got := count(t, pool, "SELECT count(*) FROM runtimes WHERE attempt_id = $1 AND role = 'verify'", c.Attempt.ID); got != 1 {
		t.Errorf("verify runtimes = %d, want 1", got)
	}
}

func TestFailuresAreClassified(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	ctx := t.Context()

	w, c := claimSpec(t, pool, svc, newSpec(t, "make"))
	if err := svc.Finish(ctx, w, c.Attempt.ID, workerapi.FinishRequest{Error: "pull image: not found", Stage: workerapi.StageImage}); err != nil {
		t.Fatal(err)
	}
	failures, err := db.New(pool).ListFailures(ctx, c.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var details map[string]string
	if len(failures) != 1 || failures[0].Category != job.CategoryEnvironment ||
		json.Unmarshal(failures[0].Details, &details) != nil || details["stage"] != workerapi.StageImage {
		t.Errorf("failures = %+v", failures)
	}

	w, c = claimSpec(t, pool, svc, newSpec(t, "make"))
	if err := svc.Executing(ctx, w, c.Attempt.ID, workerapi.ExecutingRequest{Commit: commit, Runtime: runtimeFor(workerapi.RolePrepare)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verifying(ctx, w, c.Attempt.ID, executed(137, true)); err != nil {
		t.Fatal(err)
	}
	failures, err = db.New(pool).ListFailures(ctx, c.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].Category != job.CategoryTimeout || string(failures[0].Details) == "null" {
		t.Errorf("timeout failures = %+v", failures)
	}

	w, c = claimSpec(t, pool, svc, newSpec(t, "make"))
	if err := svc.Finish(ctx, w, c.Attempt.ID, workerapi.FinishRequest{Cancelled: true}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, pool, "SELECT count(*) FROM failures WHERE job_id = $1", c.Job.ID); got != 0 {
		t.Errorf("a cancelled job recorded %d failures", got)
	}
}

func TestCancelJobThatHasNotStarted(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	sub, err := svc.Submit(t.Context(), newSpec(t, "make"), "")
	if err != nil {
		t.Fatal(err)
	}

	j, immediate, err := svc.Cancel(t.Context(), sub.Job.ID)
	if err != nil || !immediate || j.State != string(job.Cancelled) {
		t.Fatalf("Cancel: state %s immediate %v err %v", j.State, immediate, err)
	}
	if got := lastReason(t, pool, sub.Job.ID); got != "cancelled on request" {
		t.Errorf("reason = %q", got)
	}
	if _, _, err := svc.Cancel(t.Context(), sub.Job.ID); !errors.Is(err, job.ErrJobFinished) {
		t.Errorf("second Cancel: err = %v, want ErrJobFinished", err)
	}
	if _, _, err := svc.Cancel(t.Context(), uuid.New()); !errors.Is(err, job.ErrNotFound) {
		t.Errorf("unknown job: err = %v, want ErrNotFound", err)
	}
}

func TestCancelScheduledJobIsNotClaimed(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	w := addWorker(t, pool, "scheduled")
	j := submitAssigned(t, pool, "make", w)

	if _, immediate, err := svc.Cancel(t.Context(), j.ID); err != nil || !immediate {
		t.Fatalf("Cancel: immediate %v, err %v", immediate, err)
	}
	if _, found, err := svc.Claim(t.Context(), w); err != nil || found {
		t.Errorf("Claim after cancel: found %v, err %v; want nothing to claim", found, err)
	}
}

func TestCancelRunningJob(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	w, c := claimSpec(t, pool, svc, newSpec(t, "sleep 600"))
	ctx := t.Context()

	if err := svc.Executing(ctx, w, c.Attempt.ID, workerapi.ExecutingRequest{Commit: commit, Runtime: runtimeFor(workerapi.RolePrepare)}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		j, immediate, err := svc.Cancel(ctx, c.Job.ID)
		if err != nil || immediate || j.CancelRequestedAt == nil || j.State != string(job.Executing) {
			t.Fatalf("Cancel: %s requested %v immediate %v err %v", j.State, j.CancelRequestedAt, immediate, err)
		}
	}
	if got := count(t, pool, "SELECT count(*) FROM job_events WHERE job_id = $1 AND type = $2", c.Job.ID, job.EventCancelRequested); got != 1 {
		t.Errorf("cancel events = %d, want 1 for two requests", got)
	}
	ids, err := svc.CancelRequested(ctx, w)
	if err != nil || len(ids) != 1 || ids[0] != c.Attempt.ID {
		t.Fatalf("CancelRequested = %v, err %v", ids, err)
	}
	if other, err := svc.CancelRequested(ctx, addWorker(t, pool, "bystander")); err != nil || len(other) != 0 {
		t.Errorf("another worker was told to cancel %v", other)
	}

	// The worker's command ended before it heard about the cancellation: the
	// request still wins, and verification is skipped.
	verify, err := svc.Verifying(ctx, w, c.Attempt.ID, executed(0, false))
	if err != nil || verify {
		t.Fatalf("Verifying: verify %v, err %v; want the attempt ended", verify, err)
	}
	if j := jobState(t, pool, c.Job.ID); j.State != string(job.Cancelled) {
		t.Errorf("state = %s, want CANCELLED", j.State)
	}
	if a := attemptRow(t, pool, c.Job.ID); a.Status != job.AttemptCancelled {
		t.Errorf("attempt status = %s", a.Status)
	}
	if ids, _ := svc.CancelRequested(ctx, w); len(ids) != 0 {
		t.Errorf("finished attempt still listed for cancellation: %v", ids)
	}
	if _, _, err := svc.Cancel(ctx, c.Job.ID); !errors.Is(err, job.ErrJobFinished) {
		t.Errorf("Cancel after the job ended: err = %v, want ErrJobFinished", err)
	}
}

func TestCancelRequestWinsOverWorkerError(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	w, c := claimSpec(t, pool, svc, newSpec(t, "make"))
	ctx := t.Context()

	if _, _, err := svc.Cancel(ctx, c.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Finish(ctx, w, c.Attempt.ID, workerapi.FinishRequest{Error: "check out repository: context canceled", Stage: workerapi.StageCheckout}); err != nil {
		t.Fatal(err)
	}
	if j := jobState(t, pool, c.Job.ID); j.State != string(job.Cancelled) {
		t.Errorf("state = %s, want CANCELLED", j.State)
	}
	if got := count(t, pool, "SELECT count(*) FROM failures WHERE job_id = $1", c.Job.ID); got != 0 {
		t.Errorf("failures = %d, want none for a cancelled job", got)
	}
}

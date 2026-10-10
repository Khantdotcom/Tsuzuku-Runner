package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/runtime"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

const fakeCommit = "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d"

type fakeRuntime struct {
	mu         sync.Mutex
	createErr  error
	ensureErr  error
	checkout   error
	runErr     error
	exitCode   int
	timedOut   bool
	blockRun   bool
	images     []string
	steps      []runtime.Step
	repo, rev  string
	removed    []runtime.Workspace
	workspaces []runtime.Owner
}

func (f *fakeRuntime) EnsureImage(_ context.Context, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images = append(f.images, image)
	return f.ensureErr
}

func (f *fakeRuntime) CreateWorkspace(_ context.Context, owner runtime.Owner) (runtime.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return runtime.Workspace{}, f.createErr
	}
	f.workspaces = append(f.workspaces, owner)
	return runtime.Workspace{Volume: "tsuzuku-" + owner.AttemptID.String(), Owner: owner}, nil
}

func (f *fakeRuntime) Checkout(_ context.Context, _ runtime.Workspace, repo, rev string, timeout time.Duration) (string, runtime.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repo, f.rev = repo, rev
	start := time.Now()
	res := runtime.Result{
		Step:        runtime.Step{Role: workerapi.RolePrepare, Image: "alpine/git", CPUMillis: 1000, MemoryMB: 512, Network: true, Timeout: timeout},
		ContainerID: "prepare-1", StartedAt: start, FinishedAt: start.Add(time.Second),
	}
	if f.checkout != nil {
		res.ExitCode = 128
		return "", res, f.checkout
	}
	return fakeCommit, res, nil
}

func (f *fakeRuntime) Run(ctx context.Context, _ runtime.Workspace, step runtime.Step, _, _ io.Writer) (runtime.Result, error) {
	f.mu.Lock()
	f.steps = append(f.steps, step)
	block, runErr, code, timedOut := f.blockRun, f.runErr, f.exitCode, f.timedOut
	f.mu.Unlock()

	res := runtime.Result{Step: step, ContainerID: "execute-1", StartedAt: time.Now()}
	if block {
		<-ctx.Done()
		return res, ctx.Err()
	}
	if runErr != nil {
		return res, runErr
	}
	res.ExitCode, res.TimedOut = code, timedOut
	res.FinishedAt = res.StartedAt.Add(2 * time.Second)
	return res, nil
}

func (f *fakeRuntime) RemoveWorkspace(_ context.Context, ws runtime.Workspace) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, ws)
	return nil
}

// fakeReporter records reports. failures[action] lists errors returned by
// successive calls before the call succeeds.
type fakeReporter struct {
	mu        sync.Mutex
	verify    bool
	failures  map[string][]error
	calls     map[string]int
	executing []workerapi.ExecutingRequest
	verifying []workerapi.VerifyingRequest
	finished  []workerapi.FinishRequest
	finishCtx []error
}

func (f *fakeReporter) fail(action string) error {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[action]++
	if errs := f.failures[action]; len(errs) >= f.calls[action] {
		return errs[f.calls[action]-1]
	}
	return nil
}

func (f *fakeReporter) Executing(_ context.Context, _, _ uuid.UUID, req workerapi.ExecutingRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(workerapi.ActionExecuting); err != nil {
		return err
	}
	f.executing = append(f.executing, req)
	return nil
}

func (f *fakeReporter) Verifying(_ context.Context, _, _ uuid.UUID, req workerapi.VerifyingRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(workerapi.ActionVerifying); err != nil {
		return false, err
	}
	f.verifying = append(f.verifying, req)
	return f.verify, nil
}

func (f *fakeReporter) Finish(ctx context.Context, _, _ uuid.UUID, req workerapi.FinishRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(workerapi.ActionFinish); err != nil {
		return err
	}
	f.finished = append(f.finished, req)
	f.finishCtx = append(f.finishCtx, ctx.Err())
	return nil
}

func testClaim() workerapi.ClaimResponse {
	return workerapi.ClaimResponse{
		JobID: uuid.New(), JobNumber: 7, AttemptID: uuid.New(), AttemptNumber: 1,
		Spec: workload.Spec{
			Repository:     "https://github.com/example/app",
			Revision:       "main",
			Command:        "go test ./...",
			Resources:      workload.Resources{CPU: 1.5, MemoryMB: 768},
			TimeoutSeconds: 90,
			Runtime:        workload.Runtime{Image: "golang:1.27", Network: true},
		},
	}
}

func newTestExecutor(rt runtime.Runtime, rep Reporter) *Executor {
	return NewExecutor(rt, rep, slog.New(slog.DiscardHandler), ExecutorOptions{
		Worker:          "worker-test",
		CheckoutTimeout: time.Minute,
		ReportTimeout:   2 * time.Second,
		RetryDelay:      time.Millisecond,
	})
}

func TestExecutorRunsAttempt(t *testing.T) {
	rt := &fakeRuntime{}
	rep := &fakeReporter{verify: true}
	claim := testClaim()
	workerID := uuid.New()

	newTestExecutor(rt, rep).Run(t.Context(), workerID, claim)

	if len(rt.workspaces) != 1 || rt.workspaces[0] != (runtime.Owner{Worker: "worker-test", JobID: claim.JobID, AttemptID: claim.AttemptID}) {
		t.Errorf("workspace owners = %+v", rt.workspaces)
	}
	if len(rt.images) != 1 || rt.images[0] != "golang:1.27" {
		t.Errorf("pulled images = %v", rt.images)
	}
	if rt.repo != claim.Spec.Repository || rt.rev != "main" {
		t.Errorf("checked out %s@%s", rt.repo, rt.rev)
	}
	want := runtime.Step{
		Role: workerapi.RoleExecute, Image: "golang:1.27", Command: "go test ./...",
		CPUMillis: 1500, MemoryMB: 768, Network: true, Timeout: 90 * time.Second,
	}
	if len(rt.steps) != 1 || rt.steps[0] != want {
		t.Errorf("steps = %+v, want %+v", rt.steps, want)
	}

	if len(rep.executing) != 1 {
		t.Fatalf("executing reports = %d", len(rep.executing))
	}
	ex := rep.executing[0]
	if ex.Commit != fakeCommit || ex.Runtime.Role != workerapi.RolePrepare || ex.Runtime.ContainerID != "prepare-1" ||
		ex.Runtime.VolumeName != "tsuzuku-"+claim.AttemptID.String() {
		t.Errorf("executing = %+v", ex)
	}
	if len(rep.verifying) != 1 {
		t.Fatalf("verifying reports = %d", len(rep.verifying))
	}
	v := rep.verifying[0]
	if v.Execution != (workerapi.StepResult{ExitCode: 0, DurationMS: 2000}) || v.Runtime.Role != workerapi.RoleExecute ||
		v.Runtime.CPUMillis != 1500 || !v.Runtime.Network {
		t.Errorf("verifying = %+v", v)
	}
	if len(rep.finished) != 1 || rep.finished[0] != (workerapi.FinishRequest{}) {
		t.Errorf("finish = %+v, want one empty finish so the server decides", rep.finished)
	}
	if len(rt.removed) != 1 {
		t.Errorf("workspaces removed = %d, want 1", len(rt.removed))
	}
}

func TestExecutorStopsWhenServerEndsTheJob(t *testing.T) {
	rt := &fakeRuntime{exitCode: 137, timedOut: true}
	rep := &fakeReporter{verify: false}

	newTestExecutor(rt, rep).Run(t.Context(), uuid.New(), testClaim())

	if len(rep.verifying) != 1 || !rep.verifying[0].Execution.TimedOut {
		t.Fatalf("verifying = %+v", rep.verifying)
	}
	if len(rep.finished) != 0 {
		t.Errorf("finish sent after the server ended the job: %+v", rep.finished)
	}
	if len(rt.removed) != 1 {
		t.Error("workspace not removed")
	}
}

func TestExecutorReportsInfrastructureFailures(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name        string
		rt          *fakeRuntime
		wantPrefix  string
		wantRole    string
		wantRemoved int
	}{
		{"workspace", &fakeRuntime{createErr: boom}, "create workspace: boom", "", 0},
		{"image", &fakeRuntime{ensureErr: boom}, "pull image: boom", "", 1},
		{"checkout", &fakeRuntime{checkout: runtime.ErrCheckoutFailed}, "check out repository: checkout failed", workerapi.RolePrepare, 1},
		{"run", &fakeRuntime{runErr: boom}, "run command: boom", workerapi.RoleExecute, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := &fakeReporter{verify: true}
			newTestExecutor(tt.rt, rep).Run(t.Context(), uuid.New(), testClaim())

			if len(rep.finished) != 1 {
				t.Fatalf("finish reports = %d, want 1", len(rep.finished))
			}
			fin := rep.finished[0]
			if !strings.HasPrefix(fin.Error, tt.wantPrefix) || fin.Cancelled {
				t.Errorf("finish = %+v, want error starting %q", fin, tt.wantPrefix)
			}
			switch {
			case tt.wantRole == "" && fin.Runtime != nil:
				t.Errorf("runtime = %+v, want none before any container ran", fin.Runtime)
			case tt.wantRole != "" && (fin.Runtime == nil || fin.Runtime.Role != tt.wantRole):
				t.Errorf("runtime = %+v, want role %s", fin.Runtime, tt.wantRole)
			case fin.Runtime != nil && fin.Runtime.FinishedAt.Before(fin.Runtime.StartedAt):
				t.Errorf("runtime finished before it started: %+v", fin.Runtime)
			}
			if len(rep.verifying) != 0 {
				t.Error("verifying reported after a failure")
			}
			if len(tt.rt.removed) != tt.wantRemoved {
				t.Errorf("workspaces removed = %d, want %d", len(tt.rt.removed), tt.wantRemoved)
			}
		})
	}
}

func TestExecutorReportsShutdown(t *testing.T) {
	rt := &fakeRuntime{blockRun: true}
	rep := &fakeReporter{verify: true}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		newTestExecutor(rt, rep).Run(ctx, uuid.New(), testClaim())
	}()

	waitFor(t, "the command to start", func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		return len(rt.steps) == 1
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not stop after cancellation")
	}

	if len(rep.finished) != 1 || rep.finished[0].Error != "worker stopped before the attempt finished" {
		t.Fatalf("finish = %+v", rep.finished)
	}
	if rep.finishCtx[0] != nil {
		t.Error("final report was sent with a cancelled context")
	}
	if fin := rep.finished[0]; fin.Runtime == nil || fin.Runtime.FinishedAt.Before(fin.Runtime.StartedAt) {
		t.Errorf("runtime = %+v", fin.Runtime)
	}
	if len(rt.removed) != 1 {
		t.Error("workspace not removed on shutdown")
	}
}

func TestExecutorRetriesTransientReportFailures(t *testing.T) {
	rep := &fakeReporter{verify: true, failures: map[string][]error{
		workerapi.ActionExecuting: {errors.New("connection refused"), &APIError{Status: 503}},
		workerapi.ActionFinish:    {&APIError{Status: 502}},
	}}
	newTestExecutor(&fakeRuntime{}, rep).Run(t.Context(), uuid.New(), testClaim())

	if rep.calls[workerapi.ActionExecuting] != 3 || len(rep.executing) != 1 {
		t.Errorf("executing calls = %d, delivered %d", rep.calls[workerapi.ActionExecuting], len(rep.executing))
	}
	if rep.calls[workerapi.ActionFinish] != 2 || len(rep.finished) != 1 {
		t.Errorf("finish calls = %d, delivered %d", rep.calls[workerapi.ActionFinish], len(rep.finished))
	}
}

func TestExecutorStopsOnRejectedReport(t *testing.T) {
	rt := &fakeRuntime{}
	rep := &fakeReporter{verify: true, failures: map[string][]error{
		workerapi.ActionExecuting: {&APIError{Status: 409, Detail: "attempt has already finished"}},
	}}
	newTestExecutor(rt, rep).Run(t.Context(), uuid.New(), testClaim())

	if rep.calls[workerapi.ActionExecuting] != 1 {
		t.Errorf("executing calls = %d, want no retry after a 409", rep.calls[workerapi.ActionExecuting])
	}
	if len(rt.steps) != 0 || rep.calls[workerapi.ActionVerifying] != 0 || rep.calls[workerapi.ActionFinish] != 0 {
		t.Error("executor kept going after the server rejected the attempt")
	}
	if len(rt.removed) != 1 {
		t.Error("workspace not removed")
	}
}

func TestExecutorGivesUpAfterReportTimeout(t *testing.T) {
	failures := make([]error, 1000)
	for i := range failures {
		failures[i] = errors.New("connection refused")
	}
	rep := &fakeReporter{verify: true, failures: map[string][]error{workerapi.ActionExecuting: failures}}
	exec := NewExecutor(&fakeRuntime{}, rep, slog.New(slog.DiscardHandler), ExecutorOptions{
		ReportTimeout: 50 * time.Millisecond, RetryDelay: time.Millisecond,
	})

	start := time.Now()
	exec.Run(t.Context(), uuid.New(), testClaim())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("gave up after %s, want about the 50ms report timeout", elapsed)
	}
	if len(rep.executing) != 0 || rep.calls[workerapi.ActionFinish] != 0 {
		t.Errorf("reports = %+v", rep.calls)
	}
}

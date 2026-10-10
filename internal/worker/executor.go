package worker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/runtime"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

const (
	defaultCheckoutTimeout = 5 * time.Minute
	defaultReportTimeout   = time.Minute
	// shutdownReportTimeout bounds the final report when the worker is
	// stopping, so shutdown finishes within the container stop grace period.
	shutdownReportTimeout = 10 * time.Second
	maxReportRetryDelay   = 10 * time.Second
	// verifyOutputBytes is how much of the verification output is reported.
	verifyOutputBytes = 4 << 10
)

// errCancelRequested is the cause of an attempt's context when the server
// asked for the attempt to stop.
var errCancelRequested = errors.New("cancellation requested")

// Reporter sends attempt progress to the server. *Client implements it.
type Reporter interface {
	Executing(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.ExecutingRequest) error
	Verifying(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.VerifyingRequest) (bool, error)
	Finish(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.FinishRequest) error
	Logs(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.LogsRequest) (workerapi.LogsResponse, error)
}

// ExecutorOptions configures an Executor.
type ExecutorOptions struct {
	// Worker is the worker's name, used to label its containers.
	Worker string
	// CheckoutTimeout bounds cloning the repository. Zero means 5 minutes.
	CheckoutTimeout time.Duration
	// ReportTimeout bounds how long one report is retried. Zero means 1 minute.
	ReportTimeout time.Duration
	// RetryDelay is the first delay between report retries. Zero means 1s.
	RetryDelay time.Duration
	// LogInterval is the longest output waits before it is uploaded. Zero
	// means 1s.
	LogInterval time.Duration
}

// Executor carries out claimed attempts: check out, run, verify, and report.
type Executor struct {
	rt     runtime.Runtime
	api    Reporter
	logger *slog.Logger
	opts   ExecutorOptions

	mu      sync.Mutex
	running map[uuid.UUID]*runningAttempt
}

type runningAttempt struct {
	cancel    context.CancelCauseFunc
	cancelled bool
}

// NewExecutor returns an executor that runs steps on rt and reports to api.
func NewExecutor(rt runtime.Runtime, api Reporter, logger *slog.Logger, opts ExecutorOptions) *Executor {
	if opts.CheckoutTimeout <= 0 {
		opts.CheckoutTimeout = defaultCheckoutTimeout
	}
	if opts.ReportTimeout <= 0 {
		opts.ReportTimeout = defaultReportTimeout
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = time.Second
	}
	return &Executor{rt: rt, api: api, logger: logger, opts: opts, running: make(map[uuid.UUID]*runningAttempt)}
}

// Cancel stops a running attempt, which is then reported as cancelled. It
// returns true only the first time it stops the attempt.
func (e *Executor) Cancel(attemptID uuid.UUID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.running[attemptID]
	if !ok || r.cancelled {
		return false
	}
	r.cancelled = true
	r.cancel(errCancelRequested)
	return true
}

// Run carries out one claimed attempt. Every path ends with a finish report
// unless the server already ended the attempt. When ctx is cancelled the
// running container is killed and the attempt is reported as failed; when
// Cancel stops it, it is reported as cancelled.
func (e *Executor) Run(ctx context.Context, workerID uuid.UUID, claim workerapi.ClaimResponse) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	e.mu.Lock()
	e.running[claim.AttemptID] = &runningAttempt{cancel: cancel}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, claim.AttemptID)
		e.mu.Unlock()
	}()

	a := &attemptRun{
		e:        e,
		workerID: workerID,
		claim:    claim,
		log: e.logger.With("job_id", claim.JobID, "job_number", claim.JobNumber,
			"attempt_id", claim.AttemptID, "attempt", claim.AttemptNumber),
	}
	a.run(ctx)
}

type attemptRun struct {
	e        *Executor
	workerID uuid.UUID
	claim    workerapi.ClaimResponse
	log      *slog.Logger
}

func (a *attemptRun) run(ctx context.Context) {
	spec := a.claim.Spec
	rt := a.e.rt

	ws, err := rt.CreateWorkspace(ctx, runtime.Owner{Worker: a.e.opts.Worker, JobID: a.claim.JobID, AttemptID: a.claim.AttemptID})
	if err != nil {
		a.abort(ctx, workerapi.StageWorkspace, "create workspace", err, nil)
		return
	}
	defer func() {
		if err := rt.RemoveWorkspace(ctx, ws); err != nil {
			a.log.WarnContext(ctx, "remove workspace", "volume", ws.Volume, "err", err)
		}
	}()

	if err := rt.EnsureImage(ctx, spec.Runtime.Image); err != nil {
		a.abort(ctx, workerapi.StageImage, "pull image", err, nil)
		return
	}
	commit, prepared, err := rt.Checkout(ctx, ws, spec.Repository, spec.Revision, a.e.opts.CheckoutTimeout)
	if err != nil {
		a.abort(ctx, workerapi.StageCheckout, "check out repository", err, &prepared)
		return
	}
	a.log.InfoContext(ctx, "repository checked out", "commit", commit)
	err = a.report(ctx, workerapi.ActionExecuting, func(ctx context.Context) error {
		return a.e.api.Executing(ctx, a.workerID, a.claim.AttemptID, workerapi.ExecutingRequest{
			Commit:  commit,
			Runtime: runtimeRecord(ws, prepared),
		})
	})
	if err != nil {
		return
	}

	step := runtime.Step{
		Role:      workerapi.RoleExecute,
		Image:     spec.Runtime.Image,
		Command:   spec.Command,
		CPUMillis: spec.CPUMillis(),
		MemoryMB:  spec.Resources.MemoryMB,
		Network:   spec.Runtime.Network,
		Timeout:   time.Duration(spec.TimeoutSeconds) * time.Second,
	}
	ship := newLogShipper(a.sendLogs, a.log, a.e.opts.LogInterval, a.e.opts.RetryDelay)
	ship.start(ctx)
	executed, err := rt.Run(ctx, ws, step, ship.writer(workerapi.StreamStdout), ship.writer(workerapi.StreamStderr))
	ship.close(ctx, a.reportTimeout(ctx))
	if err != nil {
		a.abort(ctx, workerapi.StageExecute, "run command", err, &executed)
		return
	}
	a.log.InfoContext(ctx, "command finished", "exit_code", executed.ExitCode,
		"timed_out", executed.TimedOut, "duration", executed.Duration())

	var verify bool
	err = a.report(ctx, workerapi.ActionVerifying, func(ctx context.Context) error {
		var err error
		verify, err = a.e.api.Verifying(ctx, a.workerID, a.claim.AttemptID, workerapi.VerifyingRequest{
			Execution: workerapi.StepResult{
				ExitCode:   executed.ExitCode,
				TimedOut:   executed.TimedOut,
				DurationMS: executed.Duration().Milliseconds(),
			},
			Runtime: runtimeRecord(ws, executed),
		})
		return err
	})
	if err != nil || !verify {
		return
	}

	var req workerapi.FinishRequest
	if cmd := spec.VerificationCommand(); cmd != nil && executed.ExitCode == 0 && !executed.TimedOut {
		step.Role, step.Command = workerapi.RoleVerify, *cmd
		output := runtime.NewTailBuffer(verifyOutputBytes)
		verified, err := rt.Run(ctx, ws, step, output, output)
		if err != nil {
			a.abort(ctx, workerapi.StageVerify, "run verification", err, &verified)
			return
		}
		a.log.InfoContext(ctx, "verification finished", "exit_code", verified.ExitCode,
			"timed_out", verified.TimedOut, "duration", verified.Duration())
		req.Verification = &workerapi.VerificationResult{
			Command: *cmd,
			Result: workerapi.StepResult{
				ExitCode:   verified.ExitCode,
				TimedOut:   verified.TimedOut,
				DurationMS: verified.Duration().Milliseconds(),
			},
			Runtime:    runtimeRecord(ws, verified),
			OutputTail: output.String(),
		}
	}
	a.finish(ctx, req)
}

func (a *attemptRun) sendLogs(ctx context.Context, chunks []workerapi.LogChunk) (bool, error) {
	resp, err := a.e.api.Logs(ctx, a.workerID, a.claim.AttemptID, workerapi.LogsRequest{Chunks: chunks})
	return resp.Truncated, err
}

// abort ends the attempt early. A requested cancellation is reported as
// such; anything else is a failure outside the workload at stage.
func (a *attemptRun) abort(ctx context.Context, stage, what string, err error, res *runtime.Result) {
	var req workerapi.FinishRequest
	switch {
	case errors.Is(context.Cause(ctx), errCancelRequested):
		a.log.InfoContext(ctx, "attempt cancelled", "step", what)
		req.Cancelled = true
	case ctx.Err() != nil:
		a.log.WarnContext(ctx, "attempt stopped by shutdown", "step", what)
		req.Error, req.Stage = "worker stopped before the attempt finished", workerapi.StageShutdown
	default:
		a.log.WarnContext(ctx, "attempt aborted", "step", what, "err", err)
		req.Error, req.Stage = what+": "+err.Error(), stage
	}
	if res != nil && !res.StartedAt.IsZero() {
		rec := runtimeRecord(runtime.Workspace{}, *res)
		req.Runtime = &rec
	}
	a.finish(ctx, req)
}

// reportTimeout is how long a report may be retried: shorter once the
// attempt is being stopped.
func (a *attemptRun) reportTimeout(ctx context.Context) time.Duration {
	if ctx.Err() != nil {
		return min(a.e.opts.ReportTimeout, shutdownReportTimeout)
	}
	return a.e.opts.ReportTimeout
}

func (a *attemptRun) finish(ctx context.Context, req workerapi.FinishRequest) {
	_ = a.report(ctx, workerapi.ActionFinish, func(ctx context.Context) error {
		return a.e.api.Finish(ctx, a.workerID, a.claim.AttemptID, req)
	})
}

// report calls send until it succeeds, the server rejects it, or the report
// timeout passes. Reports outlive ctx: a stopping worker still tells the
// server how its attempts ended.
func (a *attemptRun) report(ctx context.Context, action string, send func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.reportTimeout(ctx))
	defer cancel()

	delay := a.e.opts.RetryDelay
	for {
		err := send(ctx)
		if err == nil {
			return nil
		}
		if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Status < http.StatusInternalServerError {
			a.log.WarnContext(ctx, "server rejected attempt report", "action", action, "err", err)
			return err
		}
		a.log.WarnContext(ctx, "attempt report failed; retrying", "action", action, "retry_in", delay, "err", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			a.log.ErrorContext(ctx, "gave up reporting attempt", "action", action, "err", err)
			return err
		case <-timer.C:
		}
		delay = min(delay*2, maxReportRetryDelay)
	}
}

func runtimeRecord(ws runtime.Workspace, res runtime.Result) workerapi.Runtime {
	finished := res.FinishedAt
	if finished.Before(res.StartedAt) {
		finished = time.Now()
	}
	return workerapi.Runtime{
		Role:        res.Step.Role,
		Image:       res.Step.Image,
		ContainerID: res.ContainerID,
		VolumeName:  ws.Volume,
		CPUMillis:   res.Step.CPUMillis,
		MemoryMB:    res.Step.MemoryMB,
		Network:     res.Step.Network,
		StartedAt:   res.StartedAt,
		FinishedAt:  finished,
	}
}

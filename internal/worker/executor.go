package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
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
)

// Reporter sends attempt progress to the server. *Client implements it.
type Reporter interface {
	Executing(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.ExecutingRequest) error
	Verifying(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.VerifyingRequest) (bool, error)
	Finish(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.FinishRequest) error
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
}

// Executor carries out claimed attempts: check out, run, and report.
type Executor struct {
	rt     runtime.Runtime
	api    Reporter
	logger *slog.Logger
	opts   ExecutorOptions
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
	return &Executor{rt: rt, api: api, logger: logger, opts: opts}
}

// Run carries out one claimed attempt. Every path ends with a finish report
// unless the server already ended the attempt. When ctx is cancelled the
// running container is killed and the attempt is reported as failed.
func (e *Executor) Run(ctx context.Context, workerID uuid.UUID, claim workerapi.ClaimResponse) {
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
		a.abort(ctx, "create workspace", err, nil)
		return
	}
	defer func() {
		if err := rt.RemoveWorkspace(ctx, ws); err != nil {
			a.log.WarnContext(ctx, "remove workspace", "volume", ws.Volume, "err", err)
		}
	}()

	if err := rt.EnsureImage(ctx, spec.Runtime.Image); err != nil {
		a.abort(ctx, "pull image", err, nil)
		return
	}
	commit, prepared, err := rt.Checkout(ctx, ws, spec.Repository, spec.Revision, a.e.opts.CheckoutTimeout)
	if err != nil {
		a.abort(ctx, "check out repository", err, &prepared)
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
	executed, err := rt.Run(ctx, ws, step, io.Discard, io.Discard)
	if err != nil {
		a.abort(ctx, "run command", err, &executed)
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
	a.finish(ctx, workerapi.FinishRequest{})
}

// abort ends the attempt early because of a failure outside the workload.
func (a *attemptRun) abort(ctx context.Context, what string, err error, res *runtime.Result) {
	msg := what + ": " + err.Error()
	if ctx.Err() != nil {
		msg = "worker stopped before the attempt finished"
	}
	a.log.WarnContext(ctx, "attempt aborted", "step", what, "err", err)
	req := workerapi.FinishRequest{Error: msg}
	if res != nil && !res.StartedAt.IsZero() {
		rec := runtimeRecord(runtime.Workspace{}, *res)
		req.Runtime = &rec
	}
	a.finish(ctx, req)
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
	timeout := a.e.opts.ReportTimeout
	if ctx.Err() != nil {
		timeout = min(timeout, shutdownReportTimeout)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
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

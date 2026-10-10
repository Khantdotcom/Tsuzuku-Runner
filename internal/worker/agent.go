// Package worker implements the worker agent: it registers with the API
// server, keeps sending heartbeats so the server knows it is alive, and
// claims and runs the jobs assigned to it.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

const (
	defaultMinBackoff = time.Second
	defaultMaxBackoff = 30 * time.Second
	defaultClaimWait  = 25 * time.Second
)

// API is the part of the server API the agent uses. *Client implements it.
type API interface {
	Register(ctx context.Context, req workerapi.RegisterRequest) (workerapi.RegisterResponse, error)
	Heartbeat(ctx context.Context, id uuid.UUID, req workerapi.HeartbeatRequest) (workerapi.HeartbeatResponse, error)
}

// Canceler stops a running attempt. A JobRunner that implements it receives
// the cancellations the server sends with heartbeat responses.
type Canceler interface {
	Cancel(attemptID uuid.UUID) bool
}

// JobSource hands out the jobs assigned to a worker. *Client implements it.
type JobSource interface {
	Claim(ctx context.Context, id uuid.UUID, wait time.Duration) (workerapi.ClaimResponse, bool, error)
}

// JobRunner carries out one claimed job. *Executor implements it.
type JobRunner interface {
	Run(ctx context.Context, workerID uuid.UUID, claim workerapi.ClaimResponse)
}

// Options configures an Agent.
type Options struct {
	Name              string
	Slots             int
	HeartbeatInterval time.Duration
	Version           string
	// MinBackoff and MaxBackoff bound the delay between registration and
	// claim retries. Zero values default to 1s and 30s.
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// Jobs and Runner enable job execution. When either is nil the agent
	// only registers and sends heartbeats.
	Jobs   JobSource
	Runner JobRunner
	// ClaimWait is how long one claim request waits for a job. Zero means 25s.
	ClaimWait time.Duration
}

// Agent keeps one worker registered and alive, and runs its jobs.
type Agent struct {
	api       API
	probe     Probe
	logger    *slog.Logger
	opts      Options
	lastUsage Usage

	mu sync.Mutex
	id uuid.UUID
}

// NewAgent returns an agent that talks to api and samples the host with probe.
func NewAgent(api API, probe Probe, logger *slog.Logger, opts Options) *Agent {
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = defaultMinBackoff
	}
	if opts.MaxBackoff < opts.MinBackoff {
		opts.MaxBackoff = max(defaultMaxBackoff, opts.MinBackoff)
	}
	if opts.ClaimWait <= 0 {
		opts.ClaimWait = defaultClaimWait
	}
	opts.ClaimWait = min(opts.ClaimWait, workerapi.MaxClaimWait)
	return &Agent{api: api, probe: probe, logger: logger, opts: opts}
}

func (a *Agent) setID(id uuid.UUID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.id = id
}

func (a *Agent) currentID() uuid.UUID {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.id
}

// Run registers the worker, then sends heartbeats and runs claimed jobs until
// ctx is cancelled. Registration is retried with exponential backoff, and the
// worker registers again if the server stops recognising it. On cancellation
// Run waits for running jobs to be stopped and reported, then returns nil.
func (a *Agent) Run(ctx context.Context) error {
	id, ok := a.register(ctx)
	if !ok {
		return nil
	}
	a.setID(id)

	var claiming sync.WaitGroup
	defer claiming.Wait()
	if a.opts.Jobs != nil && a.opts.Runner != nil {
		claiming.Go(func() { a.claimLoop(ctx) })
	}

	ticker := time.NewTicker(a.opts.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		resp, err := a.heartbeat(ctx, id)
		switch {
		case err == nil:
			a.cancelAttempts(ctx, resp.CancelAttempts)
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, ErrUnknownWorker):
			a.logger.WarnContext(ctx, "server does not recognise this worker; registering again", "worker_id", id)
			if id, ok = a.register(ctx); !ok {
				return nil
			}
			a.setID(id)
		default:
			a.logger.WarnContext(ctx, "heartbeat failed", "worker_id", id, "err", err)
		}
	}
}

// claimLoop keeps one claim request open whenever a slot is free and runs
// each claimed job in its own goroutine. It returns once ctx is cancelled and
// every running job has been stopped and reported.
func (a *Agent) claimLoop(ctx context.Context) {
	var running sync.WaitGroup
	defer running.Wait()

	slots := make(chan struct{}, max(a.opts.Slots, 1))
	backoff := a.opts.MinBackoff
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}

		id := a.currentID()
		claim, found, err := a.opts.Jobs.Claim(ctx, id, a.opts.ClaimWait)
		if err != nil || !found {
			<-slots
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				a.logger.WarnContext(ctx, "claim failed; retrying", "worker_id", id, "retry_in", backoff, "err", err)
				if !sleep(ctx, backoff) {
					return
				}
				backoff = min(backoff*2, a.opts.MaxBackoff)
			}
			continue
		}
		backoff = a.opts.MinBackoff

		a.logger.InfoContext(ctx, "job claimed", "job_id", claim.JobID, "job_number", claim.JobNumber,
			"attempt_id", claim.AttemptID, "attempt", claim.AttemptNumber)
		running.Go(func() {
			defer func() { <-slots }()
			a.opts.Runner.Run(ctx, id, claim)
		})
	}
}

// sleep waits for d and reports false if ctx was cancelled first.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// register retries until it succeeds or ctx is cancelled.
func (a *Agent) register(ctx context.Context) (uuid.UUID, bool) {
	backoff := a.opts.MinBackoff
	for attempt := 1; ; attempt++ {
		resp, err := a.tryRegister(ctx)
		if err == nil {
			a.logger.InfoContext(ctx, "worker registered", "worker_id", resp.ID, "slots", a.opts.Slots)
			return resp.ID, true
		}
		if ctx.Err() != nil {
			return uuid.Nil, false
		}

		a.logger.WarnContext(ctx, "registration failed; retrying", "attempt", attempt, "retry_in", backoff, "err", err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return uuid.Nil, false
		case <-timer.C:
		}
		backoff = min(backoff*2, a.opts.MaxBackoff)
	}
}

func (a *Agent) tryRegister(ctx context.Context) (workerapi.RegisterResponse, error) {
	capacity, err := a.probe.Capacity(ctx)
	if err != nil {
		return workerapi.RegisterResponse{}, err
	}
	hostname, _ := os.Hostname()
	return a.api.Register(ctx, workerapi.RegisterRequest{
		Name:      a.opts.Name,
		Slots:     a.opts.Slots,
		CPUMillis: capacity.CPUMillis,
		MemoryMB:  capacity.MemoryMB,
		Metadata: workerapi.Metadata{
			OS:       runtime.GOOS,
			Arch:     runtime.GOARCH,
			Hostname: hostname,
			Version:  a.opts.Version,
		},
	})
}

// cancelAttempts stops the listed attempts if they run here. The server keeps
// listing an attempt until it is reported, so a repeat is expected and
// harmless.
func (a *Agent) cancelAttempts(ctx context.Context, ids []uuid.UUID) {
	c, ok := a.opts.Runner.(Canceler)
	if !ok {
		return
	}
	for _, id := range ids {
		if c.Cancel(id) {
			a.logger.InfoContext(ctx, "cancelling attempt on request", "attempt_id", id)
		}
	}
}

// heartbeat sends the latest usage sample. If sampling fails it reuses the
// previous sample, since liveness matters more than fresh numbers.
func (a *Agent) heartbeat(ctx context.Context, id uuid.UUID) (workerapi.HeartbeatResponse, error) {
	if usage, err := a.probe.Usage(ctx); err != nil {
		a.logger.WarnContext(ctx, "sample host usage failed", "err", err)
	} else {
		a.lastUsage = usage
	}
	return a.api.Heartbeat(ctx, id, workerapi.HeartbeatRequest{
		CPUUsedPercent: a.lastUsage.CPUPercent,
		MemoryUsedMB:   a.lastUsage.MemoryUsedMB,
	})
}

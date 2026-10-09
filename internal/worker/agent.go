// Package worker implements the worker agent: it registers with the API
// server and keeps sending heartbeats so the server knows it is alive.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

const (
	defaultMinBackoff = time.Second
	defaultMaxBackoff = 30 * time.Second
)

// API is the part of the server API the agent uses. *Client implements it.
type API interface {
	Register(ctx context.Context, req workerapi.RegisterRequest) (workerapi.RegisterResponse, error)
	Heartbeat(ctx context.Context, id uuid.UUID, req workerapi.HeartbeatRequest) error
}

// Options configures an Agent.
type Options struct {
	Name              string
	Slots             int
	HeartbeatInterval time.Duration
	Version           string
	// MinBackoff and MaxBackoff bound the delay between registration
	// attempts. Zero values default to 1s and 30s.
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// Agent keeps one worker registered and alive.
type Agent struct {
	api       API
	probe     Probe
	logger    *slog.Logger
	opts      Options
	lastUsage Usage
}

// NewAgent returns an agent that talks to api and samples the host with probe.
func NewAgent(api API, probe Probe, logger *slog.Logger, opts Options) *Agent {
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = defaultMinBackoff
	}
	if opts.MaxBackoff < opts.MinBackoff {
		opts.MaxBackoff = max(defaultMaxBackoff, opts.MinBackoff)
	}
	return &Agent{api: api, probe: probe, logger: logger, opts: opts}
}

// Run registers the worker, then sends heartbeats until ctx is cancelled.
// Registration is retried with exponential backoff, and the worker registers
// again if the server stops recognising it. Run returns nil on cancellation.
func (a *Agent) Run(ctx context.Context) error {
	id, ok := a.register(ctx)
	if !ok {
		return nil
	}

	ticker := time.NewTicker(a.opts.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		err := a.heartbeat(ctx, id)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, ErrUnknownWorker):
			a.logger.WarnContext(ctx, "server does not recognise this worker; registering again", "worker_id", id)
			if id, ok = a.register(ctx); !ok {
				return nil
			}
		default:
			a.logger.WarnContext(ctx, "heartbeat failed", "worker_id", id, "err", err)
		}
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

// heartbeat sends the latest usage sample. If sampling fails it reuses the
// previous sample, since liveness matters more than fresh numbers.
func (a *Agent) heartbeat(ctx context.Context, id uuid.UUID) error {
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

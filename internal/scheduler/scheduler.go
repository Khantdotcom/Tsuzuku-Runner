// Package scheduler places queued jobs on online workers. Placement only
// decides where a job runs (QUEUED to SCHEDULED); the assigned worker then
// claims the job to start it.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
)

const (
	// lockKey names the advisory lock that keeps one scheduler active across
	// server replicas: "tsuzuku" in ASCII.
	lockKey int64 = 0x7473757a756b75

	defaultBatchSize = 100
)

// Options configures a Scheduler.
type Options struct {
	// Interval is the time between scheduling rounds.
	Interval time.Duration
	// StaleAfter is how old a worker's last heartbeat may be for it to count as online.
	StaleAfter time.Duration
	// BatchSize caps the jobs placed per round. Zero means 100.
	BatchSize int
	// Notifier, if set, is broadcast after a round places jobs.
	Notifier *Notifier
}

// Scheduler places queued jobs on workers.
type Scheduler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	opts   Options
}

// New returns a Scheduler that uses pool.
func New(pool *pgxpool.Pool, logger *slog.Logger, opts Options) *Scheduler {
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultBatchSize
	}
	return &Scheduler{pool: pool, logger: logger, opts: opts}
}

// Run schedules a round every Interval until ctx is cancelled. A failed round
// is logged and the next round tries again.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()
	for {
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "scheduling round failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick runs one scheduling round in a single transaction and returns what it
// placed. It places nothing when another scheduler holds the lock.
func (s *Scheduler) Tick(ctx context.Context) ([]Assignment, error) {
	var placed []Assignment
	err := store.WithTx(ctx, s.pool, func(q *db.Queries) error {
		locked, err := q.TryLockScheduler(ctx, lockKey)
		if err != nil {
			return fmt.Errorf("take scheduler lock: %w", err)
		}
		if !locked {
			return nil
		}

		rows, err := q.ListSchedulableWorkers(ctx, s.opts.StaleAfter.Seconds())
		if err != nil {
			return fmt.Errorf("list workers: %w", err)
		}
		workers := make([]Candidate, 0, len(rows))
		free := 0
		for _, r := range rows {
			c := Candidate{ID: r.ID, Name: r.Name, Slots: int(r.Slots), Active: int(r.ActiveJobs)}
			workers = append(workers, c)
			free += c.Free()
		}
		if free == 0 {
			return nil
		}

		limit := int32(min(free, s.opts.BatchSize)) //nolint:gosec // at most BatchSize
		jobs, err := q.LockQueuedJobs(ctx, limit)
		if err != nil {
			return fmt.Errorf("lock queued jobs: %w", err)
		}
		ids := make([]uuid.UUID, 0, len(jobs))
		for _, j := range jobs {
			ids = append(ids, j.ID)
		}

		assignments := Place(ids, workers)
		for _, a := range assignments {
			workerID := a.Worker.ID
			if _, err := job.Transition(ctx, q, job.Change{
				JobID:        a.JobID,
				From:         job.Queued,
				To:           job.Scheduled,
				Actor:        job.ActorScheduler,
				Reason:       fmt.Sprintf("placed on %s (%d of %d slots busy)", a.Worker.Name, a.Worker.Active, a.Worker.Slots),
				AssignWorker: &workerID,
				Details:      map[string]any{"worker_id": workerID, "worker_name": a.Worker.Name},
			}); err != nil {
				return fmt.Errorf("schedule job %s: %w", a.JobID, err)
			}
		}
		placed = assignments
		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(placed) > 0 && s.opts.Notifier != nil {
		s.opts.Notifier.Broadcast()
	}
	for _, a := range placed {
		s.logger.InfoContext(ctx, "job scheduled", "job_id", a.JobID, "worker", a.Worker.Name, "worker_id", a.Worker.ID)
	}
	return placed, nil
}

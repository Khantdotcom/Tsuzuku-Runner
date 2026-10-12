// Package scheduler places queued jobs on online workers. Placement only
// decides where a job runs (QUEUED to SCHEDULED); the assigned worker then
// claims the job to start it.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/observability/metrics"
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
	// Metrics, if set, records tick duration and placements.
	Metrics *metrics.Registry
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
// placed. It places nothing when another scheduler holds the lock. Before
// placement it recovers abandoned SCHEDULED jobs and expired mid-run leases.
func (s *Scheduler) Tick(ctx context.Context) ([]Assignment, error) {
	start := time.Now()
	var placed []Assignment
	err := store.WithTx(ctx, s.pool, func(q *db.Queries) error {
		locked, err := q.TryLockScheduler(ctx, lockKey)
		if err != nil {
			return fmt.Errorf("take scheduler lock: %w", err)
		}
		if !locked {
			return nil
		}

		if err := s.recover(ctx, q); err != nil {
			return err
		}

		rows, err := q.ListSchedulableWorkers(ctx, s.opts.StaleAfter.Seconds())
		if err != nil {
			return fmt.Errorf("list workers: %w", err)
		}
		workers := make([]Candidate, 0, len(rows))
		for _, r := range rows {
			c := Candidate{
				ID:          r.ID,
				Name:        r.Name,
				Slots:       int(r.Slots),
				Active:      int(r.ActiveJobs),
				CPUMillis:   int(r.CpuMillis),
				MemoryMB:    int(r.MemoryMB),
				ReservedCPU: int(r.ReservedCpuMillis),
				ReservedMem: int(r.ReservedMemoryMb),
			}
			if c.Free() > 0 && c.RemainingCPU() > 0 && c.RemainingMem() > 0 {
				workers = append(workers, c)
			}
		}
		if len(workers) == 0 {
			return nil
		}

		// Lock a full batch: unfit jobs are skipped, so slot count alone is not
		// a safe upper bound on how many rows to consider.
		limit := int32(s.opts.BatchSize) //nolint:gosec // BatchSize is a small positive cap
		jobs, err := q.LockQueuedJobs(ctx, limit)
		if err != nil {
			return fmt.Errorf("lock queued jobs: %w", err)
		}
		reqs := make([]JobRequest, 0, len(jobs))
		for _, j := range jobs {
			reqs = append(reqs, JobRequest{
				ID:        j.ID,
				CPUMillis: int(j.CpuMillis),
				MemoryMB:  int(j.MemoryMB),
			})
		}

		assignments := Place(reqs, workers)
		for _, a := range assignments {
			workerID := a.Worker.ID
			reason := fmt.Sprintf(
				"placed on %s (%d/%d slots, %d/%dmCPU, %d/%dMB reserved)",
				a.Worker.Name,
				a.Worker.Active, a.Worker.Slots,
				a.Worker.ReservedCPU, a.Worker.CPUMillis,
				a.Worker.ReservedMem, a.Worker.MemoryMB,
			)
			if _, err := job.Transition(ctx, q, job.Change{
				JobID:        a.JobID,
				From:         job.Queued,
				To:           job.Scheduled,
				Actor:        job.ActorScheduler,
				Reason:       reason,
				AssignWorker: &workerID,
				Details: map[string]any{
					"worker_id": workerID, "worker_name": a.Worker.Name,
					"reserved_cpu_millis": a.Worker.ReservedCPU, "worker_cpu_millis": a.Worker.CPUMillis,
					"reserved_memory_mb": a.Worker.ReservedMem, "worker_memory_mb": a.Worker.MemoryMB,
				},
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
	if s.opts.Metrics != nil {
		s.opts.Metrics.SchedulerTick.Observe(time.Since(start).Seconds())
		if n := len(placed); n > 0 {
			s.opts.Metrics.Placements.Add(float64(n))
		}
	}

	if len(placed) > 0 && s.opts.Notifier != nil {
		s.opts.Notifier.Broadcast()
	}
	for _, a := range placed {
		s.logger.InfoContext(ctx, "job scheduled", "job_id", a.JobID, "worker", a.Worker.Name, "worker_id", a.Worker.ID)
	}
	return placed, nil
}

// recover returns abandoned SCHEDULED jobs to QUEUED, fails mid-run attempts
// whose leases expired (then requeues those jobs), and promotes due RETRYING
// jobs to QUEUED. Callers must hold the scheduler advisory lock.
func (s *Scheduler) recover(ctx context.Context, q *db.Queries) error {
	limit := int32(s.opts.BatchSize) //nolint:gosec // BatchSize is a small positive cap

	abandoned, err := q.ListAbandonedScheduledJobs(ctx, db.ListAbandonedScheduledJobsParams{
		StaleSeconds: s.opts.StaleAfter.Seconds(),
		RowLimit:     limit,
	})
	if err != nil {
		return fmt.Errorf("list abandoned scheduled jobs: %w", err)
	}
	for _, j := range abandoned {
		if err := job.RequeueAbandonedScheduled(ctx, q, j.ID); err != nil {
			return fmt.Errorf("requeue abandoned scheduled job %s: %w", j.ID, err)
		}
		s.logger.InfoContext(ctx, "requeued abandoned scheduled job", "job_id", j.ID, "job_number", j.Number)
	}

	expired, err := q.ListExpiredLeases(ctx, limit)
	if err != nil {
		return fmt.Errorf("list expired leases: %w", err)
	}
	for _, row := range expired {
		if err := job.FailExpiredLease(ctx, q, row.JobID, row.AttemptID, job.State(row.JobState)); err != nil {
			return fmt.Errorf("recover expired lease on job %s: %w", row.JobID, err)
		}
		s.logger.InfoContext(ctx, "requeued job after expired lease",
			"job_id", row.JobID, "attempt_id", row.AttemptID, "worker_id", row.WorkerID)
	}

	due, err := q.LockDueRetryingJobs(ctx, limit)
	if err != nil {
		return fmt.Errorf("lock due retrying jobs: %w", err)
	}
	for _, j := range due {
		if err := job.PromoteRetrying(ctx, q, j.ID); err != nil {
			return fmt.Errorf("promote retrying job %s: %w", j.ID, err)
		}
		s.logger.InfoContext(ctx, "promoted retrying job to queued", "job_id", j.ID, "job_number", j.Number)
	}
	return nil
}

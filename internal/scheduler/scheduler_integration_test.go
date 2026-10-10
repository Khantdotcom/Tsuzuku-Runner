//go:build integration

package scheduler_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/scheduler"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/storetest"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

func TestMain(m *testing.M) {
	storetest.Main(m)
}

const staleAfter = 15 * time.Second

func newScheduler(pool *pgxpool.Pool, n *scheduler.Notifier) *scheduler.Scheduler {
	return scheduler.New(pool, slog.New(slog.DiscardHandler), scheduler.Options{
		Interval: time.Second, StaleAfter: staleAfter, Notifier: n,
	})
}

func addWorker(t *testing.T, pool *pgxpool.Pool, name string, slots int32) uuid.UUID {
	t.Helper()
	w, err := db.New(pool).CreateWorker(t.Context(), db.CreateWorkerParams{
		ID: uuid.Must(uuid.NewV7()), Name: name, Slots: slots, CpuMillis: 4000, MemoryMB: 8192, Metadata: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return w.ID
}

func submit(t *testing.T, pool *pgxpool.Pool, n int) []uuid.UUID {
	t.Helper()
	svc := job.NewService(pool)
	ids := make([]uuid.UUID, n)
	for i := range ids {
		spec, err := workload.Request{
			Repository: "https://github.com/example/app", Revision: "main", Command: fmt.Sprintf("echo %d", i),
		}.Normalize(workload.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		sub, err := svc.Submit(t.Context(), spec, "")
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = sub.Job.ID
	}
	return ids
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func getJob(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) db.Job {
	t.Helper()
	j, err := db.New(pool).GetJob(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestTickPlacesOldestJobsWithinSlots(t *testing.T) {
	pool := storetest.NewPool(t)
	a := addWorker(t, pool, "worker-a", 2)
	b := addWorker(t, pool, "worker-b", 1)
	offline := addWorker(t, pool, "worker-offline", 5)
	exec(t, pool, "UPDATE workers SET last_heartbeat_at = now() - interval '1 hour' WHERE id = $1", offline)
	jobs := submit(t, pool, 5)

	placed, err := newScheduler(pool, nil).Tick(t.Context())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(placed) != 3 {
		t.Fatalf("placed %d jobs, want 3 (two slots on worker-a, one on worker-b)", len(placed))
	}

	perWorker := map[uuid.UUID]int{}
	for i, id := range jobs {
		j := getJob(t, pool, id)
		if i < 3 {
			if j.State != string(job.Scheduled) || j.AssignedWorkerID == nil {
				t.Errorf("job %d = %s assigned to %v, want SCHEDULED and assigned", i, j.State, j.AssignedWorkerID)
				continue
			}
			perWorker[*j.AssignedWorkerID]++
		} else if j.State != string(job.Queued) || j.AssignedWorkerID != nil {
			t.Errorf("job %d = %s assigned to %v, want QUEUED and unassigned (newer jobs wait)", i, j.State, j.AssignedWorkerID)
		}
	}
	if perWorker[a] != 2 || perWorker[b] != 1 || perWorker[offline] != 0 {
		t.Errorf("jobs per worker: a=%d b=%d offline=%d, want 2, 1, 0", perWorker[a], perWorker[b], perWorker[offline])
	}

	history, err := db.New(pool).ListTransitions(t.Context(), jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	last := history[len(history)-1]
	if last.Actor != job.ActorScheduler || !strings.HasPrefix(last.Reason, "placed on worker-") {
		t.Errorf("transition = %+v, want the scheduler's placement reason", last)
	}
	events, err := db.New(pool).ListJobEvents(t.Context(), db.ListJobEventsParams{JobID: jobs[0], RowLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		WorkerName string    `json:"worker_name"`
		WorkerID   uuid.UUID `json:"worker_id"`
		To         string    `json:"to"`
	}
	if err := json.Unmarshal(events[len(events)-1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.To != string(job.Scheduled) || payload.WorkerID == uuid.Nil || payload.WorkerName == "" {
		t.Errorf("event payload = %+v", payload)
	}

	again, err := newScheduler(pool, nil).Tick(t.Context())
	if err != nil || len(again) != 0 {
		t.Errorf("second Tick placed %d (err %v), want 0 while every slot is busy", len(again), err)
	}
}

func TestTickCountsClaimedJobsAsBusy(t *testing.T) {
	pool := storetest.NewPool(t)
	w := addWorker(t, pool, "solo", 1)
	jobs := submit(t, pool, 2)
	s := newScheduler(pool, nil)

	if placed, err := s.Tick(t.Context()); err != nil || len(placed) != 1 {
		t.Fatalf("first Tick placed %d (err %v), want 1", len(placed), err)
	}
	if _, found, err := job.NewService(pool).Claim(t.Context(), w); err != nil || !found {
		t.Fatalf("Claim: found %v, err %v", found, err)
	}
	if placed, err := s.Tick(t.Context()); err != nil || len(placed) != 0 {
		t.Errorf("Tick after claim placed %d (err %v), want 0: a PREPARING job still holds the slot", len(placed), err)
	}

	exec(t, pool, "UPDATE jobs SET state = 'COMPLETED' WHERE id = $1", jobs[0])
	if placed, err := s.Tick(t.Context()); err != nil || len(placed) != 1 || placed[0].JobID != jobs[1] {
		t.Errorf("Tick after completion placed %+v (err %v), want the second job", placed, err)
	}
}

func TestTickRespectsScheduledAt(t *testing.T) {
	pool := storetest.NewPool(t)
	addWorker(t, pool, "w", 2)
	jobs := submit(t, pool, 1)
	exec(t, pool, "UPDATE jobs SET scheduled_at = now() + interval '1 hour' WHERE id = $1", jobs[0])
	s := newScheduler(pool, nil)

	if placed, err := s.Tick(t.Context()); err != nil || len(placed) != 0 {
		t.Fatalf("placed %d (err %v), want 0 before scheduled_at", len(placed), err)
	}
	exec(t, pool, "UPDATE jobs SET scheduled_at = now() - interval '1 second' WHERE id = $1", jobs[0])
	if placed, err := s.Tick(t.Context()); err != nil || len(placed) != 1 {
		t.Errorf("placed %d (err %v), want 1 once scheduled_at has passed", len(placed), err)
	}
}

func TestTickWithoutWorkers(t *testing.T) {
	pool := storetest.NewPool(t)
	jobs := submit(t, pool, 2)
	placed, err := newScheduler(pool, nil).Tick(t.Context())
	if err != nil || len(placed) != 0 {
		t.Fatalf("placed %d (err %v), want 0", len(placed), err)
	}
	if j := getJob(t, pool, jobs[0]); j.State != string(job.Queued) {
		t.Errorf("state = %s, want QUEUED", j.State)
	}
}

func TestTickBroadcastsOnlyWhenPlacing(t *testing.T) {
	pool := storetest.NewPool(t)
	addWorker(t, pool, "w", 1)
	n := scheduler.NewNotifier()
	s := newScheduler(pool, n)

	wake := n.Wait()
	if _, err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
		t.Fatal("broadcast without placing anything")
	default:
	}

	submit(t, pool, 1)
	if _, err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("no broadcast after placing a job")
	}
}

func TestConcurrentTicksNeverOverfill(t *testing.T) {
	pool := storetest.NewPool(t)
	w := addWorker(t, pool, "w", 2)
	submit(t, pool, 10)

	const rounds = 8
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		mu    sync.Mutex
		total int
	)
	for range rounds {
		wg.Go(func() {
			s := newScheduler(pool, nil)
			<-start
			placed, err := s.Tick(t.Context())
			if err != nil {
				t.Errorf("Tick: %v", err)
				return
			}
			mu.Lock()
			total += len(placed)
			mu.Unlock()
		})
	}
	close(start)
	wg.Wait()

	var assigned int
	if err := pool.QueryRow(t.Context(),
		"SELECT count(*) FROM jobs WHERE assigned_worker_id = $1 AND state = 'SCHEDULED'", w).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if total != 2 || assigned != 2 {
		t.Errorf("placed %d, assigned %d; want exactly 2 for a worker with 2 slots", total, assigned)
	}
	var doubled int
	if err := pool.QueryRow(t.Context(),
		"SELECT count(*) FROM (SELECT job_id FROM state_transitions WHERE to_state = 'SCHEDULED' GROUP BY job_id HAVING count(*) > 1) d").Scan(&doubled); err != nil {
		t.Fatal(err)
	}
	if doubled != 0 {
		t.Errorf("%d jobs were scheduled more than once", doubled)
	}
}

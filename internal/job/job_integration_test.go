//go:build integration

package job_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/storetest"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

func TestMain(m *testing.M) {
	storetest.Main(m)
}

func newSpec(t *testing.T, command string) workload.Spec {
	t.Helper()
	spec, err := workload.Request{
		Repository:         "https://github.com/example/app",
		Revision:           "main",
		Command:            command,
		AcceptanceCriteria: []string{"tests pass"},
	}.Normalize(workload.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func transition(t *testing.T, pool *pgxpool.Pool, c job.Change) (db.Job, error) {
	t.Helper()
	var j db.Job
	err := store.WithTx(t.Context(), pool, func(q *db.Queries) error {
		var err error
		j, err = job.Transition(t.Context(), q, c)
		return err
	})
	return j, err
}

func TestSubmitCreatesQueuedJob(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	spec := newSpec(t, "go test ./...")

	sub, err := svc.Submit(t.Context(), spec, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if sub.Replayed {
		t.Error("first submission reported as replayed")
	}
	if sub.Job.State != string(job.Queued) || sub.Job.Number < 1 || sub.Job.WorkloadID != sub.Workload.ID {
		t.Errorf("job = %+v", sub.Job)
	}
	if sub.Workload.IdempotencyKey != nil || sub.Workload.Image != workload.DefaultImage || sub.Workload.CpuMillis != workload.DefaultCPUMillis {
		t.Errorf("workload = %+v", sub.Workload)
	}
	if len(sub.Transitions) != 1 || sub.Transitions[0].FromState != nil || sub.Transitions[0].ToState != string(job.Queued) ||
		sub.Transitions[0].Actor != job.ActorAPI {
		t.Errorf("transitions = %+v", sub.Transitions)
	}

	for table, want := range map[string]int{"workloads": 1, "jobs": 1, "state_transitions": 1, "job_events": 1} {
		if got := count(t, pool, "SELECT count(*) FROM "+table); got != want {
			t.Errorf("%s rows = %d, want %d", table, got, want)
		}
	}
	if got := count(t, pool, "SELECT count(*) FROM job_events WHERE job_id = $1 AND type = $2", sub.Job.ID, job.EventCreated); got != 1 {
		t.Errorf("job.created events = %d, want 1", got)
	}

	found, err := svc.Find(t.Context(), job.Ref{Number: sub.Job.Number})
	if err != nil || found.ID != sub.Job.ID {
		t.Errorf("Find by number = %v, %v", found.ID, err)
	}
	detail, err := svc.Detail(t.Context(), found)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if detail.Workload.ID != sub.Workload.ID || len(detail.Transitions) != 1 {
		t.Errorf("detail = %+v", detail)
	}
}

func TestSubmitIdempotency(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	spec := newSpec(t, "go test ./...")

	first, err := svc.Submit(t.Context(), spec, "key-1")
	if err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	again, err := svc.Submit(t.Context(), newSpec(t, "go test ./..."), "key-1")
	if err != nil {
		t.Fatalf("replayed Submit: %v", err)
	}
	if !again.Replayed || again.Job.ID != first.Job.ID || len(again.Transitions) != 1 {
		t.Errorf("replay = %+v, want job %s replayed", again, first.Job.ID)
	}

	_, err = svc.Submit(t.Context(), newSpec(t, "make test"), "key-1")
	if !errors.Is(err, job.ErrIdempotencyMismatch) {
		t.Errorf("different body under the same key: err = %v, want ErrIdempotencyMismatch", err)
	}

	other, err := svc.Submit(t.Context(), spec, "key-2")
	if err != nil || other.Replayed || other.Job.ID == first.Job.ID {
		t.Errorf("new key: %+v, %v; want a new job", other.Job, err)
	}
	if got := count(t, pool, "SELECT count(*) FROM jobs"); got != 2 {
		t.Errorf("jobs = %d, want 2", got)
	}
}

func TestConcurrentSubmitsWithSameKey(t *testing.T) {
	pool := storetest.NewPool(t)
	svc := job.NewService(pool)
	spec := newSpec(t, "go test ./...")

	const n = 20
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		ids   = make([]uuid.UUID, n)
		errs  = make([]error, n)
	)
	for i := range n {
		wg.Go(func() {
			<-start
			sub, err := svc.Submit(t.Context(), spec, "same-key")
			ids[i], errs[i] = sub.Job.ID, err
		})
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		if ids[i] != ids[0] {
			t.Fatalf("submit %d returned job %s, want %s", i, ids[i], ids[0])
		}
	}
	if got := count(t, pool, "SELECT count(*) FROM jobs"); got != 1 {
		t.Errorf("jobs = %d, want 1", got)
	}
	if got := count(t, pool, "SELECT count(*) FROM job_events"); got != 1 {
		t.Errorf("job_events = %d, want 1", got)
	}
}

func TestConcurrentTransitionsHaveOneWinner(t *testing.T) {
	pool := storetest.NewPool(t)
	sub, err := job.NewService(pool).Submit(t.Context(), newSpec(t, "go test ./..."), "")
	if err != nil {
		t.Fatal(err)
	}

	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		errs  = make([]error, 2)
	)
	for i := range errs {
		wg.Go(func() {
			<-start
			_, errs[i] = transition(t, pool, job.Change{
				JobID: sub.Job.ID, From: job.Queued, To: job.Scheduled, Actor: "test", Reason: "race",
			})
		})
	}
	close(start)
	wg.Wait()

	var wins, conflicts int
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, job.ErrConflict):
			conflicts++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Errorf("wins = %d, conflicts = %d; want 1 and 1 (errors: %v)", wins, conflicts, errs)
	}
	if got := count(t, pool, "SELECT count(*) FROM state_transitions WHERE job_id = $1", sub.Job.ID); got != 2 {
		t.Errorf("transitions = %d, want 2", got)
	}
	if got := count(t, pool, "SELECT count(*) FROM job_events WHERE type = $1", job.EventStateChanged); got != 1 {
		t.Errorf("state change events = %d, want 1", got)
	}
}

func TestTransitionLifecycle(t *testing.T) {
	pool := storetest.NewPool(t)
	sub, err := job.NewService(pool).Submit(t.Context(), newSpec(t, "go test ./..."), "")
	if err != nil {
		t.Fatal(err)
	}
	id := sub.Job.ID

	steps := []job.State{job.Scheduled, job.Preparing, job.Executing, job.Verifying, job.Completed}
	from := job.Queued
	var j db.Job
	for _, to := range steps {
		j, err = transition(t, pool, job.Change{JobID: id, From: from, To: to, Actor: "test", Reason: "step"})
		if err != nil {
			t.Fatalf("%s to %s: %v", from, to, err)
		}
		if j.State != string(to) {
			t.Fatalf("state = %s, want %s", j.State, to)
		}
		switch to {
		case job.Scheduled:
			if j.StartedAt != nil {
				t.Error("started_at set before PREPARING")
			}
		case job.Preparing:
			if j.StartedAt == nil {
				t.Error("started_at not set on PREPARING")
			}
		}
		if (j.FinishedAt != nil) != to.Terminal() {
			t.Errorf("after %s: finished_at = %v", to, j.FinishedAt)
		}
		from = to
	}
	if j.FinishedAt.Before(*j.StartedAt) {
		t.Errorf("finished_at %v before started_at %v", j.FinishedAt, j.StartedAt)
	}

	q := db.New(pool)
	history, err := q.ListTransitions(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != len(steps)+1 {
		t.Fatalf("history = %d entries, want %d", len(history), len(steps)+1)
	}
	for i, to := range steps {
		h := history[i+1]
		if h.FromState == nil || h.ToState != string(to) || h.Actor != "test" {
			t.Errorf("history[%d] = %+v, want to %s", i+1, h, to)
		}
	}
	events, err := q.ListJobEvents(t.Context(), db.ListJobEventsParams{JobID: id, AfterID: 0, RowLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(steps)+1 || events[0].Type != job.EventCreated || events[1].Type != job.EventStateChanged {
		t.Errorf("events = %+v", events)
	}
}

func TestTransitionFailureFromPreparingSetsFinished(t *testing.T) {
	pool := storetest.NewPool(t)
	sub, err := job.NewService(pool).Submit(t.Context(), newSpec(t, "go test ./..."), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []job.Change{
		{From: job.Queued, To: job.Scheduled},
		{From: job.Scheduled, To: job.Preparing},
	} {
		c.JobID, c.Actor = sub.Job.ID, "test"
		if _, err := transition(t, pool, c); err != nil {
			t.Fatal(err)
		}
	}
	j, err := transition(t, pool, job.Change{JobID: sub.Job.ID, From: job.Preparing, To: job.Failed, Actor: "test", Reason: "clone failed"})
	if err != nil {
		t.Fatal(err)
	}
	if j.StartedAt == nil || j.FinishedAt == nil {
		t.Errorf("started_at = %v, finished_at = %v; want both set", j.StartedAt, j.FinishedAt)
	}
}

func TestTransitionRejections(t *testing.T) {
	pool := storetest.NewPool(t)
	sub, err := job.NewService(pool).Submit(t.Context(), newSpec(t, "go test ./..."), "")
	if err != nil {
		t.Fatal(err)
	}
	id := sub.Job.ID

	tests := []struct {
		name   string
		change job.Change
		want   error
	}{
		{"illegal edge", job.Change{JobID: id, From: job.Queued, To: job.Completed, Actor: "test"}, job.ErrIllegalTransition},
		{"stale from state", job.Change{JobID: id, From: job.Scheduled, To: job.Preparing, Actor: "test"}, job.ErrConflict},
		{"unknown job", job.Change{JobID: uuid.Must(uuid.NewV7()), From: job.Queued, To: job.Scheduled, Actor: "test"}, job.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := transition(t, pool, tt.change); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}

	if _, err := transition(t, pool, job.Change{JobID: id, From: job.Queued, To: job.Scheduled}); err == nil {
		t.Error("transition without an actor succeeded")
	}

	j, err := db.New(pool).GetJob(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != string(job.Queued) {
		t.Errorf("state = %s after rejected transitions, want QUEUED", j.State)
	}
	if got := count(t, pool, "SELECT count(*) FROM state_transitions WHERE job_id = $1", id); got != 1 {
		t.Errorf("transitions = %d, want only the initial one", got)
	}
	if got := count(t, pool, "SELECT count(*) FROM job_events WHERE type = $1", job.EventStateChanged); got != 0 {
		t.Errorf("state change events = %d, want 0", got)
	}
}

func TestFindUnknownJob(t *testing.T) {
	svc := job.NewService(storetest.NewPool(t))
	for _, ref := range []job.Ref{{ID: uuid.Must(uuid.NewV7())}, {Number: 999}} {
		if _, err := svc.Find(t.Context(), ref); !errors.Is(err, job.ErrNotFound) {
			t.Errorf("Find(%+v) err = %v, want ErrNotFound", ref, err)
		}
	}
}

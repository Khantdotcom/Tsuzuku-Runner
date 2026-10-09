//go:build integration

package store_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/storetest"
)

const (
	checkViolation      = "23514"
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
)

func TestMain(m *testing.M) {
	storetest.Main(m)
}

func TestMigrationsUpDownUp(t *testing.T) {
	ctx := t.Context()
	migrator, err := store.Migrator(storetest.OpenSQL(t, storetest.NewDatabase(t)))
	if err != nil {
		t.Fatal(err)
	}

	up, err := migrator.Up(ctx)
	if err != nil {
		t.Fatalf("first up: %v", err)
	}
	if len(up) == 0 {
		t.Fatal("first up applied no migrations")
	}
	if _, err := migrator.DownTo(ctx, 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}
	again, err := migrator.Up(ctx)
	if err != nil {
		t.Fatalf("second up: %v", err)
	}
	if len(again) != len(up) {
		t.Fatalf("second up applied %d migrations, want %d", len(again), len(up))
	}
}

func TestJobStateMustBeKnown(t *testing.T) {
	pool := storetest.NewPool(t)
	f := newFixture(t, pool)

	_, err := pool.Exec(t.Context(), "UPDATE jobs SET state = 'DONE' WHERE id = $1", f.job.ID)
	requireSQLState(t, err, checkViolation)
}

func TestJobRequiresExistingWorkload(t *testing.T) {
	q := db.New(storetest.NewPool(t))

	_, err := q.CreateJob(t.Context(), db.CreateJobParams{ID: uuid.Must(uuid.NewV7()), WorkloadID: uuid.Must(uuid.NewV7())})
	requireSQLState(t, err, foreignKeyViolation)
}

func TestJobNumbersIncrease(t *testing.T) {
	pool := storetest.NewPool(t)
	f := newFixture(t, pool)

	second, err := db.New(pool).CreateJob(t.Context(), db.CreateJobParams{ID: uuid.Must(uuid.NewV7()), WorkloadID: f.workload.ID})
	if err != nil {
		t.Fatal(err)
	}
	if second.Number <= f.job.Number {
		t.Fatalf("second job number %d is not greater than first %d", second.Number, f.job.Number)
	}
}

func TestWorkloadIdempotencyKeyIsUnique(t *testing.T) {
	ctx := t.Context()
	q := db.New(storetest.NewPool(t))

	key := "submit-1"
	if _, err := q.CreateWorkload(ctx, workloadParams(&key)); err != nil {
		t.Fatal(err)
	}
	_, err := q.CreateWorkload(ctx, workloadParams(&key))
	requireSQLState(t, err, uniqueViolation)

	for range 2 {
		if _, err := q.CreateWorkload(ctx, workloadParams(nil)); err != nil {
			t.Fatalf("workloads without a key must not conflict: %v", err)
		}
	}
}

func TestOnlyOneActiveLeasePerJob(t *testing.T) {
	ctx := t.Context()
	pool := storetest.NewPool(t)
	f := newFixture(t, pool)
	q := db.New(pool)

	lease := func() (db.Lease, error) {
		return q.AcquireLease(ctx, db.AcquireLeaseParams{
			ID:        uuid.Must(uuid.NewV7()),
			JobID:     f.job.ID,
			AttemptID: f.attempt.ID,
			WorkerID:  f.worker.ID,
			ExpiresAt: time.Now().Add(time.Minute),
		})
	}

	first, err := lease()
	if err != nil {
		t.Fatal(err)
	}
	_, err = lease()
	requireSQLState(t, err, uniqueViolation)

	if _, err := pool.Exec(ctx, "UPDATE leases SET released_at = now() WHERE id = $1", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := lease(); err != nil {
		t.Fatalf("lease after release: %v", err)
	}
}

func TestLogChunkSeqIsUniquePerAttempt(t *testing.T) {
	ctx := t.Context()
	pool := storetest.NewPool(t)
	f := newFixture(t, pool)
	q := db.New(pool)

	chunk := db.AppendLogChunkParams{JobID: f.job.ID, AttemptID: f.attempt.ID, Seq: 0, Stream: "stdout", Data: []byte("hello\n")}
	if _, err := q.AppendLogChunk(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	chunk.Stream = "stderr"
	_, err := q.AppendLogChunk(ctx, chunk)
	requireSQLState(t, err, uniqueViolation)
}

func TestTransitionJobIsCompareAndSet(t *testing.T) {
	ctx := t.Context()
	pool := storetest.NewPool(t)
	f := newFixture(t, pool)

	err := store.WithTx(ctx, pool, func(q *db.Queries) error {
		job, err := q.TransitionJob(ctx, db.TransitionJobParams{ID: f.job.ID, FromState: "QUEUED", ToState: "SCHEDULED"})
		if err != nil {
			return err
		}
		from := "QUEUED"
		_, err = q.RecordTransition(ctx, db.RecordTransitionParams{
			JobID: job.ID, FromState: &from, ToState: job.State, Actor: "test", Reason: "placed",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	q := db.New(pool)
	_, err = q.TransitionJob(ctx, db.TransitionJobParams{ID: f.job.ID, FromState: "QUEUED", ToState: "CANCELLED"})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale transition: got %v, want pgx.ErrNoRows", err)
	}

	job, err := q.GetJob(ctx, f.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "SCHEDULED" {
		t.Fatalf("state = %s, want SCHEDULED", job.State)
	}
	history, err := q.ListTransitions(ctx, f.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ToState != "SCHEDULED" {
		t.Fatalf("history = %+v, want one transition to SCHEDULED", history)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	ctx := t.Context()
	pool := storetest.NewPool(t)
	errBoom := errors.New("boom")

	err := store.WithTx(ctx, pool, func(q *db.Queries) error {
		if _, err := q.CreateWorkload(ctx, workloadParams(nil)); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("WithTx error = %v, want %v", err, errBoom)
	}

	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM workloads").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("workloads = %d after rollback, want 0", n)
	}
}

func TestColumnRoundTrips(t *testing.T) {
	ctx := t.Context()
	pool := storetest.NewPool(t)
	f := newFixture(t, pool)
	q := db.New(pool)

	if !slices.Equal(f.workload.AcceptanceCriteria, []string{"tests pass", "no new warnings"}) {
		t.Errorf("acceptance criteria = %q", f.workload.AcceptanceCriteria)
	}

	var spec, metadata map[string]any
	if err := json.Unmarshal(f.workload.Spec, &spec); err != nil || spec["command"] != "go test ./..." {
		t.Errorf("spec = %s (%v)", f.workload.Spec, err)
	}
	if err := json.Unmarshal(f.worker.Metadata, &metadata); err != nil || metadata["os"] != "linux" {
		t.Errorf("metadata = %s (%v)", f.worker.Metadata, err)
	}

	raw := []byte{0xff, 0xfe, 'o', 'k', 0x00, '\n'}
	if _, err := q.AppendLogChunk(ctx, db.AppendLogChunkParams{
		JobID: f.job.ID, AttemptID: f.attempt.ID, Seq: 0, Stream: "stdout", Data: raw,
	}); err != nil {
		t.Fatal(err)
	}
	chunks, err := q.ListLogChunks(ctx, db.ListLogChunksParams{JobID: f.job.ID, AfterID: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || !bytes.Equal(chunks[0].Data, raw) {
		t.Fatalf("log chunks = %+v, want one chunk with %x", chunks, raw)
	}
	after, err := q.ListLogChunks(ctx, db.ListLogChunksParams{JobID: f.job.ID, AfterID: chunks[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("chunks after last id = %d, want 0", len(after))
	}
}

type fixture struct {
	worker   db.Worker
	workload db.Workload
	job      db.Job
	attempt  db.JobAttempt
}

// newFixture inserts one worker, workload, job, and attempt.
func newFixture(t *testing.T, pool *pgxpool.Pool) fixture {
	t.Helper()
	ctx := t.Context()
	q := db.New(pool)

	var f fixture
	var err error
	f.worker, err = q.CreateWorker(ctx, db.CreateWorkerParams{
		ID: uuid.Must(uuid.NewV7()), Name: "worker-" + uuid.NewString()[:8],
		Slots: 1, CpuMillis: 1000, MemoryMB: 1024, Metadata: json.RawMessage(`{"os": "linux"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.workload, err = q.CreateWorkload(ctx, workloadParams(nil))
	if err != nil {
		t.Fatal(err)
	}
	f.job, err = q.CreateJob(ctx, db.CreateJobParams{ID: uuid.Must(uuid.NewV7()), WorkloadID: f.workload.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.attempt, err = q.CreateAttempt(ctx, db.CreateAttemptParams{
		ID: uuid.Must(uuid.NewV7()), JobID: f.job.ID, AttemptNumber: 1, WorkerID: f.worker.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func workloadParams(idempotencyKey *string) db.CreateWorkloadParams {
	return db.CreateWorkloadParams{
		ID:                 uuid.Must(uuid.NewV7()),
		IdempotencyKey:     idempotencyKey,
		RepositoryURL:      "https://github.com/Khantdotcom/tsuzuku-sample-go",
		Revision:           "main",
		Command:            "go test ./...",
		Image:              "golang:1.27",
		AcceptanceCriteria: []string{"tests pass", "no new warnings"},
		CpuMillis:          1000,
		MemoryMB:           1024,
		TimeoutSeconds:     600,
		NetworkEnabled:     true,
		Spec:               json.RawMessage(`{"command": "go test ./..."}`),
	}
}

func requireSQLState(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want PostgreSQL error %s", err, code)
	}
	if pgErr.Code != code {
		t.Fatalf("SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.Message, code)
	}
}

//go:build integration

package devseed_test

import (
	"maps"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Khantdotcom/tsuzuku-runner/internal/devseed"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/storetest"
)

func TestMain(m *testing.M) {
	storetest.Main(m)
}

func TestApplyIsIdempotent(t *testing.T) {
	ctx := t.Context()
	pool := storetest.NewPool(t)

	applied, err := devseed.Apply(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("first Apply reported no changes")
	}
	applied, err = devseed.Apply(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("second Apply inserted data again")
	}

	rows, err := db.New(pool).CountJobsByState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, r := range rows {
		got[r.State] = r.Count
	}
	want := map[string]int64{"CANCELLED": 1, "COMPLETED": 1, "FAILED": 1}
	if !maps.Equal(got, want) {
		t.Fatalf("jobs by state = %v, want %v", got, want)
	}
}

func TestSeededHistoryEndsInJobState(t *testing.T) {
	ctx := t.Context()
	pool := storetest.NewPool(t)
	if _, err := devseed.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	q := db.New(pool)
	rows, err := pool.Query(ctx, "SELECT id FROM jobs ORDER BY number")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		job, err := q.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		history, err := q.ListTransitions(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) == 0 || history[0].FromState != nil {
			t.Fatalf("job #%d: history must start with a creation transition", job.Number)
		}
		for i := 1; i < len(history); i++ {
			if prev := history[i-1].ToState; history[i].FromState == nil || *history[i].FromState != prev {
				t.Fatalf("job #%d: transition %d does not continue from %s", job.Number, i, prev)
			}
		}
		if last := history[len(history)-1].ToState; last != job.State {
			t.Fatalf("job #%d: last transition to %s, job state %s", job.Number, last, job.State)
		}
	}
}

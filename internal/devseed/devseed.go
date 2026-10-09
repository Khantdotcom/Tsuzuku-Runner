// Package devseed loads example job history into a development database.
//
// The data covers finished jobs only (COMPLETED, FAILED, CANCELLED) owned by an
// offline worker, so a running scheduler never picks any of it up.
package devseed

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkerName is the name of the offline worker that owns the seeded history.
const WorkerName = "seed-worker"

//go:embed seed.sql
var seedSQL string

// Apply inserts the example data in a single transaction. It returns false and
// changes nothing when the data is already present.
func Apply(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	applied := false
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM workers WHERE name = $1)", WorkerName).Scan(&exists); err != nil {
			return fmt.Errorf("check for existing seed data: %w", err)
		}
		if exists {
			return nil
		}
		// Without arguments, pgx uses the simple protocol, which allows multiple statements.
		if _, err := tx.Exec(ctx, seedSQL); err != nil {
			return fmt.Errorf("insert seed data: %w", err)
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

//go:build integration

// Package storetest runs integration tests against a disposable PostgreSQL
// container. Each test package shares one container; each test gets its own
// database, so tests stay isolated without paying for a container per test.
package storetest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
)

// Image matches the PostgreSQL version in compose.yaml.
const Image = "postgres:17-alpine"

var (
	adminURL string
	dbCount  atomic.Int64
)

// Main starts the container, runs the package's tests, and removes the
// container. Call it from TestMain.
func Main(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("tsuzuku"),
		postgres.WithUsername("tsuzuku"),
		postgres.WithPassword("tsuzuku"),
		postgres.BasicWaitStrategies(),
	)
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintln(os.Stderr, "storetest: terminate postgres:", err)
		}
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "storetest: start postgres (is Docker running?):", err)
		return 1
	}

	adminURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "storetest: connection string:", err)
		return 1
	}
	return m.Run()
}

// NewDatabase creates an empty database and returns its connection URL.
func NewDatabase(t testing.TB) string {
	t.Helper()
	if adminURL == "" {
		t.Fatal("storetest: call storetest.Main from TestMain")
	}
	ctx := t.Context()

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	defer conn.Close(ctx)

	name := fmt.Sprintf("test_%d", dbCount.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("parse connection URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// OpenSQL opens a database/sql handle, which goose requires.
func OpenSQL(t testing.TB, dbURL string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// NewPool creates a fully migrated database and returns a pool connected to it.
func NewPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	dbURL := NewDatabase(t)

	migrator, err := store.Migrator(OpenSQL(t, dbURL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Up(t.Context()); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := store.Open(t.Context(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Command migrate applies the embedded schema migrations to PostgreSQL.
//
// Usage:
//
//	migrate up            apply all pending migrations
//	migrate down          roll back the most recent migration
//	migrate status        list migrations and whether each is applied
//	migrate create NAME   add an empty SQL migration under ./migrations
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"

	"github.com/Khantdotcom/tsuzuku-runner/internal/config"
	"github.com/Khantdotcom/tsuzuku-runner/internal/observability/logging"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
)

const usage = "usage: migrate up | down | status | create NAME"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "create" {
		if len(args) != 2 {
			return errors.New("usage: migrate create NAME")
		}
		goose.SetSequential(true)
		return goose.Create(nil, "migrations", args[1], "sql")
	}
	if len(args) != 1 {
		return errors.New(usage)
	}

	cfg, err := config.LoadDBTool()
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.Env, cfg.LogLevel, "migrate")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, err := sql.Open("pgx", cfg.URL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer conn.Close()

	migrator, err := store.Migrator(conn)
	if err != nil {
		return err
	}

	switch args[0] {
	case "up":
		results, err := migrator.Up(ctx)
		for _, r := range results {
			logger.Info("applied migration", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
		}
		if err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		if len(results) == 0 {
			logger.Info("schema is up to date")
		}
	case "down":
		r, err := migrator.Down(ctx)
		if err != nil {
			return fmt.Errorf("migrate down: %w", err)
		}
		logger.Info("rolled back migration", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
	case "status":
		statuses, err := migrator.Status(ctx)
		if err != nil {
			return fmt.Errorf("migrate status: %w", err)
		}
		return printStatus(statuses)
	default:
		return errors.New(usage)
	}
	return nil
}

func printStatus(statuses []*goose.MigrationStatus) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VERSION\tFILE\tSTATE\tAPPLIED AT")
	for _, s := range statuses {
		appliedAt := ""
		if s.State == goose.StateApplied {
			appliedAt = s.AppliedAt.Local().Format(time.DateTime)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", s.Source.Version, s.Source.Path, s.State, appliedAt)
	}
	return w.Flush()
}

// Command seed loads example job history into a development database. It is
// safe to rerun: it does nothing when the seed data is already present.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Khantdotcom/tsuzuku-runner/internal/config"
	"github.com/Khantdotcom/tsuzuku-runner/internal/devseed"
	"github.com/Khantdotcom/tsuzuku-runner/internal/observability/logging"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadDBTool()
	if err != nil {
		return err
	}
	if cfg.Env == config.Production {
		return errors.New("refusing to seed a production database")
	}
	logger := logging.New(os.Stdout, cfg.Env, cfg.LogLevel, "seed")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := store.Open(ctx, cfg.URL)
	if err != nil {
		return err
	}
	defer pool.Close()

	applied, err := devseed.Apply(ctx, pool)
	if err != nil {
		return err
	}
	if applied {
		logger.Info("seed data inserted")
	} else {
		logger.Info("seed data already present; nothing to do")
	}
	return nil
}

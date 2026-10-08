// Command worker runs a Tsuzuku worker that executes jobs assigned by the scheduler.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Khantdotcom/tsuzuku-runner/internal/config"
	"github.com/Khantdotcom/tsuzuku-runner/internal/observability/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.Env, cfg.LogLevel, "worker").With("worker", cfg.Name)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("worker started", "heartbeat_interval", cfg.HeartbeatInterval, "env", cfg.Env)

	ticker := time.NewTicker(cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("worker stopped")
			return nil
		case <-ticker.C:
			logger.Info("worker tick")
		}
	}
}

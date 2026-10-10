// Command worker runs a Tsuzuku worker. It registers with the API server,
// sends heartbeats, and runs its assigned jobs in Docker containers; it never
// connects to the database directly.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/Khantdotcom/tsuzuku-runner/internal/config"
	"github.com/Khantdotcom/tsuzuku-runner/internal/observability/logging"
	"github.com/Khantdotcom/tsuzuku-runner/internal/runtime"
	"github.com/Khantdotcom/tsuzuku-runner/internal/worker"
)

const dockerStartupTimeout = 30 * time.Second

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

	startCtx, cancelStart := context.WithTimeout(ctx, dockerStartupTimeout)
	defer cancelStart()
	docker, err := runtime.NewDocker(startCtx, cfg.GitImage, logger)
	if err != nil {
		return err
	}
	defer func() { _ = docker.Close() }()
	removed, err := docker.CleanupLeftovers(startCtx, cfg.Name)
	if err != nil {
		return fmt.Errorf("remove leftover containers: %w", err)
	}

	logger.Info("worker started",
		"api_url", cfg.APIURL,
		"slots", cfg.Slots,
		"heartbeat_interval", cfg.HeartbeatInterval,
		"git_image", cfg.GitImage,
		"leftovers_removed", removed,
		"env", cfg.Env,
	)

	client := worker.NewClient(cfg.APIURL, cfg.Token)
	executor := worker.NewExecutor(docker, client, logger, worker.ExecutorOptions{
		Worker:          cfg.Name,
		CheckoutTimeout: cfg.CheckoutTimeout,
	})
	agent := worker.NewAgent(client, worker.HostProbe{}, logger, worker.Options{
		Name:              cfg.Name,
		Slots:             cfg.Slots,
		HeartbeatInterval: cfg.HeartbeatInterval,
		Version:           version(),
		Jobs:              client,
		Runner:            executor,
	})
	err = agent.Run(ctx)
	logger.Info("worker stopped")
	return err
}

// version reports the module version, or the VCS revision for local builds.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 12 {
			return s.Value[:12]
		}
	}
	return "dev"
}

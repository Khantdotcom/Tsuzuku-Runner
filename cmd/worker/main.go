// Command worker runs a Tsuzuku worker. It registers with the API server and
// sends heartbeats; it never connects to the database directly.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/Khantdotcom/tsuzuku-runner/internal/config"
	"github.com/Khantdotcom/tsuzuku-runner/internal/observability/logging"
	"github.com/Khantdotcom/tsuzuku-runner/internal/worker"
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

	logger.Info("worker started",
		"api_url", cfg.APIURL,
		"slots", cfg.Slots,
		"heartbeat_interval", cfg.HeartbeatInterval,
		"env", cfg.Env,
	)

	agent := worker.NewAgent(
		worker.NewClient(cfg.APIURL, cfg.Token),
		worker.HostProbe{},
		logger,
		worker.Options{
			Name:              cfg.Name,
			Slots:             cfg.Slots,
			HeartbeatInterval: cfg.HeartbeatInterval,
			Version:           version(),
		},
	)
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

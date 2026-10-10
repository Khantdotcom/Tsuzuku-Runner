// Command server runs the Tsuzuku API server.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Khantdotcom/tsuzuku-runner/internal/api"
	"github.com/Khantdotcom/tsuzuku-runner/internal/config"
	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/observability/logging"
	"github.com/Khantdotcom/tsuzuku-runner/internal/scheduler"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadServer()
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.Env, cfg.LogLevel, "server")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := store.Open(ctx, cfg.URL)
	if err != nil {
		return err
	}
	defer pool.Close()

	jobs := job.NewService(pool)
	assignments := scheduler.NewNotifier()
	stopping := make(chan struct{})

	router := api.NewRouter(api.Config{
		Logger:           logger,
		DB:               pool,
		Workers:          db.New(pool),
		Jobs:             jobs,
		Claimer:          jobs,
		Assignments:      assignments,
		Stopping:         stopping,
		WorkerToken:      cfg.Token,
		WorkerStaleAfter: cfg.WorkerStaleAfter,
		WorkloadLimits: workload.Limits{
			MaxCPUMillis: cfg.MaxCPUMillis,
			MaxMemoryMB:  cfg.MaxMemoryMB,
			MaxTimeout:   cfg.MaxTimeout,
		},
	})

	// No WriteTimeout: later milestones stream logs over long-lived SSE connections.
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	srv.RegisterOnShutdown(func() { close(stopping) })

	sched := scheduler.New(pool, logger, scheduler.Options{
		Interval:   cfg.SchedulerInterval,
		StaleAfter: cfg.WorkerStaleAfter,
		Notifier:   assignments,
	})
	schedCtx, stopSched := context.WithCancel(ctx)
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		sched.Run(schedCtx)
	}()
	// Runs before pool.Close, so no scheduling round outlives the pool.
	defer func() {
		stopSched()
		<-schedDone
	}()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down", "timeout", cfg.ShutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("server stopped")
	return nil
}

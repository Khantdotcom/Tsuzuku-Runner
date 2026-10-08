// Package config loads typed process configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
)

// Environment identifies the deployment environment a process runs in.
type Environment string

const (
	Development Environment = "development"
	Test        Environment = "test"
	Production  Environment = "production"
)

func (e Environment) valid() bool {
	switch e {
	case Development, Test, Production:
		return true
	default:
		return false
	}
}

// Common holds settings shared by every Tsuzuku process.
type Common struct {
	Env      Environment `env:"TSUZUKU_ENV"       envDefault:"development"`
	LogLevel slog.Level  `env:"TSUZUKU_LOG_LEVEL" envDefault:"info"`
}

// Server configures the API server process.
type Server struct {
	Common

	HTTPAddr        string        `env:"TSUZUKU_HTTP_ADDR"        envDefault:":8080"`
	ShutdownTimeout time.Duration `env:"TSUZUKU_SHUTDOWN_TIMEOUT" envDefault:"10s"`
}

// Worker configures the worker process.
type Worker struct {
	Common

	// Name defaults to the host name when unset.
	Name              string        `env:"TSUZUKU_WORKER_NAME"`
	HeartbeatInterval time.Duration `env:"TSUZUKU_WORKER_HEARTBEAT_INTERVAL" envDefault:"5s"`
}

// LoadServer reads Server configuration from the process environment.
func LoadServer() (Server, error) {
	return loadServer(env.ToMap(os.Environ()))
}

// LoadWorker reads Worker configuration from the process environment.
func LoadWorker() (Worker, error) {
	return loadWorker(env.ToMap(os.Environ()))
}

func loadServer(environ map[string]string) (Server, error) {
	cfg, err := env.ParseAsWithOptions[Server](env.Options{Environment: environ})
	if err != nil {
		return Server{}, fmt.Errorf("parse server config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Server{}, err
	}
	if cfg.ShutdownTimeout <= 0 {
		return Server{}, fmt.Errorf("TSUZUKU_SHUTDOWN_TIMEOUT must be positive, got %s", cfg.ShutdownTimeout)
	}
	return cfg, nil
}

func loadWorker(environ map[string]string) (Worker, error) {
	cfg, err := env.ParseAsWithOptions[Worker](env.Options{Environment: environ})
	if err != nil {
		return Worker{}, fmt.Errorf("parse worker config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Worker{}, err
	}
	if cfg.HeartbeatInterval <= 0 {
		return Worker{}, fmt.Errorf("TSUZUKU_WORKER_HEARTBEAT_INTERVAL must be positive, got %s", cfg.HeartbeatInterval)
	}
	if cfg.Name == "" {
		host, err := os.Hostname()
		if err != nil {
			return Worker{}, fmt.Errorf("resolve default worker name: %w", err)
		}
		cfg.Name = host
	}
	return cfg, nil
}

func (c Common) validate() error {
	if !c.Env.valid() {
		return fmt.Errorf("TSUZUKU_ENV must be one of development, test, production; got %q", c.Env)
	}
	return nil
}

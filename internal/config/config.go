// Package config loads typed process configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"math"
	"net/url"
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

// minProductionTokenLen is the shortest worker token accepted in production.
const minProductionTokenLen = 32

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

// Database holds PostgreSQL connection settings. Workers never load it: they
// reach the database only through the API.
type Database struct {
	URL string `env:"TSUZUKU_DATABASE_URL,required,notEmpty"`
}

// WorkerAuth holds the shared bearer token workers present to the API.
type WorkerAuth struct {
	Token string `env:"TSUZUKU_WORKER_TOKEN,required,notEmpty"`
}

// WorkloadLimits are the most resources a single workload may request.
type WorkloadLimits struct {
	MaxCPUMillis int           `env:"TSUZUKU_WORKLOAD_MAX_CPU_MILLIS" envDefault:"4000"`
	MaxMemoryMB  int           `env:"TSUZUKU_WORKLOAD_MAX_MEMORY_MB"  envDefault:"8192"`
	MaxTimeout   time.Duration `env:"TSUZUKU_WORKLOAD_MAX_TIMEOUT"    envDefault:"1h"`
}

// Server configures the API server process.
type Server struct {
	Common
	Database
	WorkerAuth
	WorkloadLimits

	HTTPAddr        string        `env:"TSUZUKU_HTTP_ADDR"        envDefault:":8080"`
	ShutdownTimeout time.Duration `env:"TSUZUKU_SHUTDOWN_TIMEOUT" envDefault:"10s"`
	// WorkerStaleAfter is how long after its last heartbeat a worker is reported offline.
	WorkerStaleAfter time.Duration `env:"TSUZUKU_WORKER_STALE_AFTER" envDefault:"15s"`
}

// Worker configures the worker process.
type Worker struct {
	Common
	WorkerAuth

	// Name defaults to the host name when unset.
	Name              string        `env:"TSUZUKU_WORKER_NAME"`
	APIURL            string        `env:"TSUZUKU_API_URL"                   envDefault:"http://localhost:8080"`
	Slots             int           `env:"TSUZUKU_WORKER_SLOTS"              envDefault:"2"`
	HeartbeatInterval time.Duration `env:"TSUZUKU_WORKER_HEARTBEAT_INTERVAL" envDefault:"5s"`
}

// DBTool configures one-shot database commands such as migrate and seed.
type DBTool struct {
	Common
	Database
}

// LoadServer reads Server configuration from the process environment.
func LoadServer() (Server, error) {
	return loadServer(env.ToMap(os.Environ()))
}

// LoadWorker reads Worker configuration from the process environment.
func LoadWorker() (Worker, error) {
	return loadWorker(env.ToMap(os.Environ()))
}

// LoadDBTool reads DBTool configuration from the process environment.
func LoadDBTool() (DBTool, error) {
	return loadDBTool(env.ToMap(os.Environ()))
}

func loadServer(environ map[string]string) (Server, error) {
	cfg, err := env.ParseAsWithOptions[Server](env.Options{Environment: environ})
	if err != nil {
		return Server{}, fmt.Errorf("parse server config: %w", err)
	}
	if err := cfg.Common.validate(); err != nil {
		return Server{}, err
	}
	if err := cfg.WorkerAuth.validate(cfg.Env); err != nil {
		return Server{}, err
	}
	if cfg.ShutdownTimeout <= 0 {
		return Server{}, fmt.Errorf("TSUZUKU_SHUTDOWN_TIMEOUT must be positive, got %s", cfg.ShutdownTimeout)
	}
	if cfg.WorkerStaleAfter <= 0 {
		return Server{}, fmt.Errorf("TSUZUKU_WORKER_STALE_AFTER must be positive, got %s", cfg.WorkerStaleAfter)
	}
	if err := cfg.WorkloadLimits.validate(); err != nil {
		return Server{}, err
	}
	return cfg, nil
}

func loadWorker(environ map[string]string) (Worker, error) {
	cfg, err := env.ParseAsWithOptions[Worker](env.Options{Environment: environ})
	if err != nil {
		return Worker{}, fmt.Errorf("parse worker config: %w", err)
	}
	if err := cfg.Common.validate(); err != nil {
		return Worker{}, err
	}
	if err := cfg.WorkerAuth.validate(cfg.Env); err != nil {
		return Worker{}, err
	}
	if cfg.HeartbeatInterval <= 0 {
		return Worker{}, fmt.Errorf("TSUZUKU_WORKER_HEARTBEAT_INTERVAL must be positive, got %s", cfg.HeartbeatInterval)
	}
	if cfg.Slots <= 0 {
		return Worker{}, fmt.Errorf("TSUZUKU_WORKER_SLOTS must be positive, got %d", cfg.Slots)
	}
	if u, err := url.Parse(cfg.APIURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Worker{}, fmt.Errorf("TSUZUKU_API_URL must be an http(s) URL with a host, got %q", cfg.APIURL)
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

func loadDBTool(environ map[string]string) (DBTool, error) {
	cfg, err := env.ParseAsWithOptions[DBTool](env.Options{Environment: environ})
	if err != nil {
		return DBTool{}, fmt.Errorf("parse database config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return DBTool{}, err
	}
	return cfg, nil
}

func (c Common) validate() error {
	if !c.Env.valid() {
		return fmt.Errorf("TSUZUKU_ENV must be one of development, test, production; got %q", c.Env)
	}
	return nil
}

// The lower bounds match the smallest workload the API accepts; the upper
// bounds keep the values within the database's integer columns.
func (l WorkloadLimits) validate() error {
	switch {
	case l.MaxCPUMillis < 100 || l.MaxCPUMillis > math.MaxInt32:
		return fmt.Errorf("TSUZUKU_WORKLOAD_MAX_CPU_MILLIS must be between 100 and %d, got %d", math.MaxInt32, l.MaxCPUMillis)
	case l.MaxMemoryMB < 64 || l.MaxMemoryMB > math.MaxInt32:
		return fmt.Errorf("TSUZUKU_WORKLOAD_MAX_MEMORY_MB must be between 64 and %d, got %d", math.MaxInt32, l.MaxMemoryMB)
	case l.MaxTimeout < time.Second || l.MaxTimeout/time.Second > math.MaxInt32:
		return fmt.Errorf("TSUZUKU_WORKLOAD_MAX_TIMEOUT must be between 1s and %d seconds, got %s", math.MaxInt32, l.MaxTimeout)
	}
	return nil
}

func (a WorkerAuth) validate(e Environment) error {
	if e == Production && len(a.Token) < minProductionTokenLen {
		return fmt.Errorf("TSUZUKU_WORKER_TOKEN must be at least %d characters in production", minProductionTokenLen)
	}
	return nil
}

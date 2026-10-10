package config

import (
	"log/slog"
	"maps"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	testDatabaseURL = "postgres://tsuzuku:tsuzuku@127.0.0.1:5433/tsuzuku?sslmode=disable"
	testToken       = "dev-worker-token"
	productionToken = "0123456789abcdef0123456789abcdef"
)

// serverEnv returns the minimal valid server environment with overrides applied.
func serverEnv(overrides map[string]string) map[string]string {
	environ := map[string]string{
		"TSUZUKU_DATABASE_URL": testDatabaseURL,
		"TSUZUKU_WORKER_TOKEN": testToken,
	}
	maps.Copy(environ, overrides)
	return environ
}

// workerEnv returns the minimal valid worker environment with overrides applied.
func workerEnv(overrides map[string]string) map[string]string {
	environ := map[string]string{"TSUZUKU_WORKER_TOKEN": testToken}
	maps.Copy(environ, overrides)
	return environ
}

func TestLoadServerDefaults(t *testing.T) {
	cfg, err := loadServer(serverEnv(nil))
	if err != nil {
		t.Fatalf("loadServer: %v", err)
	}

	if cfg.Env != Development {
		t.Errorf("Env = %q, want %q", cfg.Env, Development)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, 10*time.Second)
	}
	if cfg.WorkerStaleAfter != 15*time.Second {
		t.Errorf("WorkerStaleAfter = %v, want %v", cfg.WorkerStaleAfter, 15*time.Second)
	}
	if cfg.URL != testDatabaseURL {
		t.Errorf("URL = %q, want %q", cfg.URL, testDatabaseURL)
	}
	if cfg.Token != testToken {
		t.Errorf("Token = %q, want %q", cfg.Token, testToken)
	}
	if want := (WorkloadLimits{MaxCPUMillis: 4000, MaxMemoryMB: 8192, MaxTimeout: time.Hour}); cfg.WorkloadLimits != want {
		t.Errorf("WorkloadLimits = %+v, want %+v", cfg.WorkloadLimits, want)
	}
	if cfg.SchedulerInterval != time.Second {
		t.Errorf("SchedulerInterval = %s, want 1s", cfg.SchedulerInterval)
	}
}

func TestLoadServerWorkloadLimits(t *testing.T) {
	cfg, err := loadServer(serverEnv(map[string]string{
		"TSUZUKU_WORKLOAD_MAX_CPU_MILLIS": "8000",
		"TSUZUKU_WORKLOAD_MAX_MEMORY_MB":  "16384",
		"TSUZUKU_WORKLOAD_MAX_TIMEOUT":    "2h",
	}))
	if err != nil {
		t.Fatalf("loadServer: %v", err)
	}
	if want := (WorkloadLimits{MaxCPUMillis: 8000, MaxMemoryMB: 16384, MaxTimeout: 2 * time.Hour}); cfg.WorkloadLimits != want {
		t.Errorf("WorkloadLimits = %+v, want %+v", cfg.WorkloadLimits, want)
	}
}

func TestLoadServerOverrides(t *testing.T) {
	cfg, err := loadServer(serverEnv(map[string]string{
		"TSUZUKU_ENV":                "production",
		"TSUZUKU_LOG_LEVEL":          "debug",
		"TSUZUKU_HTTP_ADDR":          "127.0.0.1:9090",
		"TSUZUKU_SHUTDOWN_TIMEOUT":   "30s",
		"TSUZUKU_WORKER_STALE_AFTER": "1m",
		"TSUZUKU_WORKER_TOKEN":       productionToken,
	}))
	if err != nil {
		t.Fatalf("loadServer: %v", err)
	}

	if cfg.Env != Production {
		t.Errorf("Env = %q, want %q", cfg.Env, Production)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, "127.0.0.1:9090")
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, 30*time.Second)
	}
	if cfg.WorkerStaleAfter != time.Minute {
		t.Errorf("WorkerStaleAfter = %v, want %v", cfg.WorkerStaleAfter, time.Minute)
	}
}

func TestLoadServerRejectsInvalidValues(t *testing.T) {
	// Parse failures name the struct field or variable; validation failures name the variable.
	tests := []struct {
		name    string
		environ map[string]string
		wantErr string
	}{
		{"unknown env", serverEnv(map[string]string{"TSUZUKU_ENV": "staging"}), "TSUZUKU_ENV"},
		{"bad log level", serverEnv(map[string]string{"TSUZUKU_LOG_LEVEL": "loud"}), "LogLevel"},
		{"bad duration", serverEnv(map[string]string{"TSUZUKU_SHUTDOWN_TIMEOUT": "soon"}), "ShutdownTimeout"},
		{"zero timeout", serverEnv(map[string]string{"TSUZUKU_SHUTDOWN_TIMEOUT": "0s"}), "TSUZUKU_SHUTDOWN_TIMEOUT"},
		{"zero stale after", serverEnv(map[string]string{"TSUZUKU_WORKER_STALE_AFTER": "0s"}), "TSUZUKU_WORKER_STALE_AFTER"},
		{"missing database", map[string]string{"TSUZUKU_WORKER_TOKEN": testToken}, "TSUZUKU_DATABASE_URL"},
		{"missing token", map[string]string{"TSUZUKU_DATABASE_URL": testDatabaseURL}, "TSUZUKU_WORKER_TOKEN"},
		{"short production token", serverEnv(map[string]string{"TSUZUKU_ENV": "production"}), "TSUZUKU_WORKER_TOKEN"},
		{"tiny cpu limit", serverEnv(map[string]string{"TSUZUKU_WORKLOAD_MAX_CPU_MILLIS": "50"}), "TSUZUKU_WORKLOAD_MAX_CPU_MILLIS"},
		{"huge cpu limit", serverEnv(map[string]string{"TSUZUKU_WORKLOAD_MAX_CPU_MILLIS": "3000000000"}), "3000000000"},
		{"tiny memory limit", serverEnv(map[string]string{"TSUZUKU_WORKLOAD_MAX_MEMORY_MB": "10"}), "TSUZUKU_WORKLOAD_MAX_MEMORY_MB"},
		{"sub-second timeout limit", serverEnv(map[string]string{"TSUZUKU_WORKLOAD_MAX_TIMEOUT": "500ms"}), "TSUZUKU_WORKLOAD_MAX_TIMEOUT"},
		{"huge timeout limit", serverEnv(map[string]string{"TSUZUKU_WORKLOAD_MAX_TIMEOUT": "600000h"}), "TSUZUKU_WORKLOAD_MAX_TIMEOUT"},
		{"tiny scheduler interval", serverEnv(map[string]string{"TSUZUKU_SCHEDULER_INTERVAL": "10ms"}), "TSUZUKU_SCHEDULER_INTERVAL"},
		{"negative scheduler interval", serverEnv(map[string]string{"TSUZUKU_SCHEDULER_INTERVAL": "-1s"}), "TSUZUKU_SCHEDULER_INTERVAL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadServer(tt.environ)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadWorkerDefaults(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("hostname unavailable: %v", err)
	}

	cfg, err := loadWorker(workerEnv(nil))
	if err != nil {
		t.Fatalf("loadWorker: %v", err)
	}

	if cfg.Name != host {
		t.Errorf("Name = %q, want hostname %q", cfg.Name, host)
	}
	if cfg.HeartbeatInterval != 5*time.Second {
		t.Errorf("HeartbeatInterval = %v, want %v", cfg.HeartbeatInterval, 5*time.Second)
	}
	if cfg.APIURL != "http://localhost:8080" {
		t.Errorf("APIURL = %q, want %q", cfg.APIURL, "http://localhost:8080")
	}
	if cfg.Slots != 2 {
		t.Errorf("Slots = %d, want 2", cfg.Slots)
	}
}

func TestLoadWorkerOverrides(t *testing.T) {
	cfg, err := loadWorker(workerEnv(map[string]string{
		"TSUZUKU_WORKER_NAME":               "worker-01",
		"TSUZUKU_WORKER_HEARTBEAT_INTERVAL": "2s",
		"TSUZUKU_API_URL":                   "https://tsuzuku.example.com",
		"TSUZUKU_WORKER_SLOTS":              "4",
	}))
	if err != nil {
		t.Fatalf("loadWorker: %v", err)
	}

	if cfg.Name != "worker-01" {
		t.Errorf("Name = %q, want %q", cfg.Name, "worker-01")
	}
	if cfg.HeartbeatInterval != 2*time.Second {
		t.Errorf("HeartbeatInterval = %v, want %v", cfg.HeartbeatInterval, 2*time.Second)
	}
	if cfg.APIURL != "https://tsuzuku.example.com" {
		t.Errorf("APIURL = %q, want %q", cfg.APIURL, "https://tsuzuku.example.com")
	}
	if cfg.Slots != 4 {
		t.Errorf("Slots = %d, want 4", cfg.Slots)
	}
}

func TestLoadWorkerRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		environ map[string]string
		wantErr string
	}{
		{"missing token", map[string]string{}, "TSUZUKU_WORKER_TOKEN"},
		{"negative heartbeat", workerEnv(map[string]string{"TSUZUKU_WORKER_HEARTBEAT_INTERVAL": "-1s"}), "TSUZUKU_WORKER_HEARTBEAT_INTERVAL"},
		{"zero slots", workerEnv(map[string]string{"TSUZUKU_WORKER_SLOTS": "0"}), "TSUZUKU_WORKER_SLOTS"},
		{"relative api url", workerEnv(map[string]string{"TSUZUKU_API_URL": "localhost:8080"}), "TSUZUKU_API_URL"},
		{"non-http api url", workerEnv(map[string]string{"TSUZUKU_API_URL": "ftp://example.com"}), "TSUZUKU_API_URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadWorker(tt.environ)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadDBTool(t *testing.T) {
	cfg, err := loadDBTool(map[string]string{"TSUZUKU_DATABASE_URL": testDatabaseURL})
	if err != nil {
		t.Fatalf("loadDBTool: %v", err)
	}
	if cfg.URL != testDatabaseURL {
		t.Errorf("URL = %q, want %q", cfg.URL, testDatabaseURL)
	}
	if cfg.Env != Development {
		t.Errorf("Env = %q, want %q", cfg.Env, Development)
	}
}

func TestLoadDBToolRequiresURL(t *testing.T) {
	for name, environ := range map[string]map[string]string{
		"unset": {},
		"empty": {"TSUZUKU_DATABASE_URL": ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadDBTool(environ)
			if err == nil || !strings.Contains(err.Error(), "TSUZUKU_DATABASE_URL") {
				t.Fatalf("expected TSUZUKU_DATABASE_URL error, got %v", err)
			}
		})
	}
}

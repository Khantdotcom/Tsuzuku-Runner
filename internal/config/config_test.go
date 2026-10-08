package config

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadServerDefaults(t *testing.T) {
	cfg, err := loadServer(map[string]string{})
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
}

func TestLoadServerOverrides(t *testing.T) {
	cfg, err := loadServer(map[string]string{
		"TSUZUKU_ENV":              "production",
		"TSUZUKU_LOG_LEVEL":        "debug",
		"TSUZUKU_HTTP_ADDR":        "127.0.0.1:9090",
		"TSUZUKU_SHUTDOWN_TIMEOUT": "30s",
	})
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
}

func TestLoadServerRejectsInvalidValues(t *testing.T) {
	// Parse failures name the struct field; validation failures name the variable.
	tests := []struct {
		name    string
		environ map[string]string
		wantErr string
	}{
		{"unknown env", map[string]string{"TSUZUKU_ENV": "staging"}, "TSUZUKU_ENV"},
		{"bad log level", map[string]string{"TSUZUKU_LOG_LEVEL": "loud"}, "LogLevel"},
		{"bad duration", map[string]string{"TSUZUKU_SHUTDOWN_TIMEOUT": "soon"}, "ShutdownTimeout"},
		{"zero timeout", map[string]string{"TSUZUKU_SHUTDOWN_TIMEOUT": "0s"}, "TSUZUKU_SHUTDOWN_TIMEOUT"},
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

func TestLoadWorkerDefaultsNameToHostname(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("hostname unavailable: %v", err)
	}

	cfg, err := loadWorker(map[string]string{})
	if err != nil {
		t.Fatalf("loadWorker: %v", err)
	}

	if cfg.Name != host {
		t.Errorf("Name = %q, want hostname %q", cfg.Name, host)
	}
	if cfg.HeartbeatInterval != 5*time.Second {
		t.Errorf("HeartbeatInterval = %v, want %v", cfg.HeartbeatInterval, 5*time.Second)
	}
}

func TestLoadWorkerOverrides(t *testing.T) {
	cfg, err := loadWorker(map[string]string{
		"TSUZUKU_WORKER_NAME":               "worker-01",
		"TSUZUKU_WORKER_HEARTBEAT_INTERVAL": "2s",
	})
	if err != nil {
		t.Fatalf("loadWorker: %v", err)
	}

	if cfg.Name != "worker-01" {
		t.Errorf("Name = %q, want %q", cfg.Name, "worker-01")
	}
	if cfg.HeartbeatInterval != 2*time.Second {
		t.Errorf("HeartbeatInterval = %v, want %v", cfg.HeartbeatInterval, 2*time.Second)
	}
}

func TestLoadWorkerRejectsNonPositiveHeartbeat(t *testing.T) {
	_, err := loadWorker(map[string]string{"TSUZUKU_WORKER_HEARTBEAT_INTERVAL": "-1s"})
	if err == nil || !strings.Contains(err.Error(), "TSUZUKU_WORKER_HEARTBEAT_INTERVAL") {
		t.Fatalf("expected heartbeat interval error, got %v", err)
	}
}

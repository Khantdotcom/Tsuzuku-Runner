package workload

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

const validRequest = `{"repository":"https://github.com/Khantdotcom/tsuzuku-sample-go","revision":"main","command":"go test ./..."}`

func decode(t *testing.T, body string) Request {
	t.Helper()
	var r Request
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return r
}

func TestNormalizeAppliesDefaults(t *testing.T) {
	spec, err := decode(t, validRequest).Normalize(DefaultLimits())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	want := Spec{
		Repository:         "https://github.com/Khantdotcom/tsuzuku-sample-go",
		Revision:           "main",
		Command:            "go test ./...",
		AcceptanceCriteria: []string{},
		Resources:          Resources{CPU: 1, MemoryMB: 1024},
		TimeoutSeconds:     600,
		Runtime:            Runtime{Image: "golang:1.27", Network: true},
	}
	if !reflect.DeepEqual(spec, want) {
		t.Fatalf("spec = %+v\nwant   %+v", spec, want)
	}
	if spec.CPUMillis() != 1000 || spec.VerificationCommand() != nil {
		t.Errorf("CPUMillis = %d, VerificationCommand = %v", spec.CPUMillis(), spec.VerificationCommand())
	}
}

func TestNormalizeKeepsExplicitValues(t *testing.T) {
	body := `{
		"repository": "https://gitlab.com/group/sub/project.git",
		"revision": "feature/x-1",
		"command": "npm test",
		"acceptance_criteria": ["tests pass", "no lint errors"],
		"resources": {"cpu": 0.5, "memory_mb": 2048},
		"timeout_seconds": 3600,
		"runtime": {"image": "node", "network": false},
		"verification": {"command": "npm run lint"}
	}`
	spec, err := decode(t, body).Normalize(DefaultLimits())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	want := Spec{
		Repository:         "https://gitlab.com/group/sub/project.git",
		Revision:           "feature/x-1",
		Command:            "npm test",
		AcceptanceCriteria: []string{"tests pass", "no lint errors"},
		Resources:          Resources{CPU: 0.5, MemoryMB: 2048},
		TimeoutSeconds:     3600,
		Runtime:            Runtime{Image: "node:latest", Network: false},
		Verification:       &Verification{Command: "npm run lint"},
	}
	if !reflect.DeepEqual(spec, want) {
		t.Fatalf("spec = %+v\nwant   %+v", spec, want)
	}
	if spec.CPUMillis() != 500 {
		t.Errorf("CPUMillis = %d, want 500", spec.CPUMillis())
	}
	if cmd := spec.VerificationCommand(); cmd == nil || *cmd != "npm run lint" {
		t.Errorf("VerificationCommand = %v", cmd)
	}
}

func TestNormalizeLowersDefaultsToLimits(t *testing.T) {
	limits := Limits{MaxCPUMillis: 500, MaxMemoryMB: 512, MaxTimeout: time.Minute}
	spec, err := decode(t, validRequest).Normalize(limits)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if spec.Resources.CPU != 0.5 || spec.Resources.MemoryMB != 512 || spec.TimeoutSeconds != 60 {
		t.Errorf("defaults = %+v timeout %d, want capped at the limits", spec.Resources, spec.TimeoutSeconds)
	}
}

func TestNormalizeSpecRoundTripsThroughJSON(t *testing.T) {
	body := `{"repository":"https://github.com/a/b","revision":"v1.2.3","command":"pytest",
		"resources":{"cpu":1.25},"runtime":{"image":"python:3.13-slim"},"verification":{"command":"ruff check"}}`
	spec, err := decode(t, body).Normalize(DefaultLimits())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var back Spec
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spec, back) {
		t.Fatalf("round trip changed the spec:\n%+v\n%+v", spec, back)
	}
}

func TestNormalizeRejectsInvalidRequests(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	base := map[string]any{
		"repository": "https://github.com/a/b",
		"revision":   "main",
		"command":    "go test ./...",
	}
	with := func(key string, value any) string {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		if value == nil {
			delete(m, key)
		} else {
			m[key] = value
		}
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"missing repository", with("repository", nil), "repository"},
		{"http repository", with("repository", "http://github.com/a/b"), "repository"},
		{"ssh repository", with("repository", "git@github.com:a/b.git"), "repository"},
		{"repository with credentials", with("repository", "https://user:pass@github.com/a/b"), "repository"},
		{"repository with query", with("repository", "https://github.com/a/b?x=1"), "repository"},
		{"repository with fragment", with("repository", "https://github.com/a/b#main"), "repository"},
		{"repository without host", with("repository", "https:///a/b"), "repository"},
		{"repository with space", with("repository", "https://github.com/a b"), "repository"},
		{"repository too long", with("repository", "https://github.com/"+long(2048)), "repository"},
		{"missing revision", with("revision", nil), "revision"},
		{"option-like revision", with("revision", "--upload-pack=x"), "revision"},
		{"revision with space", with("revision", "main branch"), "revision"},
		{"revision with semicolon", with("revision", "main;rm"), "revision"},
		{"revision too long", with("revision", long(256)), "revision"},
		{"missing command", with("command", nil), "command"},
		{"blank command", with("command", "   \n"), "command"},
		{"command with NUL", with("command", "go\x00test"), "command"},
		{"command too long", with("command", long(4097)), "command"},
		{"too many criteria", with("acceptance_criteria", make([]string, 21)), "acceptance_criteria"},
		{"blank criterion", with("acceptance_criteria", []string{"ok", " "}), "acceptance_criteria[1]"},
		{"criterion too long", with("acceptance_criteria", []string{long(501)}), "acceptance_criteria[0]"},
		{"cpu too low", with("resources", map[string]any{"cpu": 0.05}), "resources.cpu"},
		{"cpu above limit", with("resources", map[string]any{"cpu": 4.5}), "resources.cpu"},
		{"cpu too precise", with("resources", map[string]any{"cpu": 1.0005}), "resources.cpu"},
		{"memory too low", with("resources", map[string]any{"memory_mb": 32}), "resources.memory_mb"},
		{"memory above limit", with("resources", map[string]any{"memory_mb": 8193}), "resources.memory_mb"},
		{"zero timeout", with("timeout_seconds", 0), "timeout_seconds"},
		{"timeout above limit", with("timeout_seconds", 3601), "timeout_seconds"},
		{"image not allowed", with("runtime", map[string]any{"image": "ubuntu:24.04"}), "runtime.image"},
		{"image from other registry", with("runtime", map[string]any{"image": "ghcr.io/golang:1.27"}), "runtime.image"},
		{"image with digest", with("runtime", map[string]any{"image": "golang@sha256:abc"}), "runtime.image"},
		{"image with empty tag", with("runtime", map[string]any{"image": "golang:"}), "runtime.image"},
		{"blank verification command", with("verification", map[string]any{"command": ""}), "verification.command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decode(t, tt.body).Normalize(DefaultLimits())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestNormalizeAcceptsBoundaryValues(t *testing.T) {
	body := `{"repository":"https://github.com/a/b","revision":"0123abc","command":"x",
		"resources":{"cpu":0.1,"memory_mb":64},"timeout_seconds":1}`
	spec, err := decode(t, body).Normalize(DefaultLimits())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if spec.CPUMillis() != 100 || spec.Resources.MemoryMB != 64 || spec.TimeoutSeconds != 1 {
		t.Errorf("spec = %+v", spec)
	}

	body = `{"repository":"https://github.com/a/b","revision":"main","command":"x",
		"resources":{"cpu":4,"memory_mb":8192},"timeout_seconds":3600}`
	if _, err := decode(t, body).Normalize(DefaultLimits()); err != nil {
		t.Fatalf("maximum values rejected: %v", err)
	}
}

func TestValidateIdempotencyKey(t *testing.T) {
	for _, key := range []string{"a", "order-123", "2f1c:retry/1", strings.Repeat("k", 255)} {
		if err := ValidateIdempotencyKey(key); err != nil {
			t.Errorf("ValidateIdempotencyKey(%q) = %v", key, err)
		}
	}
	for _, key := range []string{"", strings.Repeat("k", 256), "has space", "tab\t", "caf\u00e9"} {
		if err := ValidateIdempotencyKey(key); err == nil {
			t.Errorf("ValidateIdempotencyKey(%q) accepted an invalid key", key)
		}
	}
}

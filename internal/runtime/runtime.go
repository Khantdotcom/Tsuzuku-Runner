// Package runtime runs workload steps in isolated, resource-limited containers
// that share a per-attempt workspace volume.
package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

// WorkspaceDir is where the checked-out repository is mounted in every step.
const WorkspaceDir = "/workspace"

// Labels put on every container and volume the runtime creates, so leftovers
// can be found and removed after a crash.
const (
	LabelManaged = "dev.tsuzuku.managed"
	LabelWorker  = "dev.tsuzuku.worker"
	LabelJob     = "dev.tsuzuku.job"
	LabelAttempt = "dev.tsuzuku.attempt"
	LabelRole    = "dev.tsuzuku.role"
)

const (
	pidsLimit         = 512
	checkoutCPUMillis = 1000
	checkoutMemoryMB  = 512
	tailBytes         = 4 << 10
)

// checkoutScript reads the repository and revision from the environment so
// neither is ever interpreted by the shell.
const checkoutScript = `set -eu
git init -q .
git remote add origin "$TSZ_REPO"
if git fetch -q --depth 1 origin "$TSZ_REV" 2>/dev/null; then
  git checkout -q --detach FETCH_HEAD
else
  git fetch -q origin '+refs/heads/*:refs/remotes/origin/*' '+refs/tags/*:refs/tags/*'
  git checkout -q --detach "$TSZ_REV"
fi
git rev-parse HEAD`

// ErrCheckoutFailed reports that git could not fetch or check out the revision.
var ErrCheckoutFailed = errors.New("checkout failed")

// Runtime prepares workspaces and runs steps in them. *Docker implements it.
type Runtime interface {
	EnsureImage(ctx context.Context, image string) error
	CreateWorkspace(ctx context.Context, owner Owner) (Workspace, error)
	// Checkout clones repo at rev into the workspace and returns the resolved
	// commit hash.
	Checkout(ctx context.Context, ws Workspace, repo, rev string, timeout time.Duration) (string, Result, error)
	// Run executes step in the workspace. A non-zero exit code or a timeout is
	// a Result, not an error; errors mean the runtime itself failed.
	Run(ctx context.Context, ws Workspace, step Step, stdout, stderr io.Writer) (Result, error)
	RemoveWorkspace(ctx context.Context, ws Workspace) error
}

// Owner identifies who a workspace and its containers belong to.
type Owner struct {
	Worker    string
	JobID     uuid.UUID
	AttemptID uuid.UUID
}

func (o Owner) labels(role string) map[string]string {
	return map[string]string{
		LabelManaged: "true",
		LabelWorker:  o.Worker,
		LabelJob:     o.JobID.String(),
		LabelAttempt: o.AttemptID.String(),
		LabelRole:    role,
	}
}

// Workspace is a volume holding one attempt's checkout.
type Workspace struct {
	Volume string
	Owner  Owner
}

func workspaceName(attemptID uuid.UUID) string {
	return "tsuzuku-" + attemptID.String()
}

func containerName(ws Workspace, role string) string {
	return workspaceName(ws.Owner.AttemptID) + "-" + role
}

// Step is one container run inside a workspace.
type Step struct {
	Role      string
	Image     string
	Command   string
	CPUMillis int
	MemoryMB  int
	Network   bool
	Timeout   time.Duration
}

// Result describes how a step's container ended.
type Result struct {
	Step        Step
	ContainerID string
	ExitCode    int
	TimedOut    bool
	StartedAt   time.Time
	FinishedAt  time.Time
}

// Duration is how long the container ran.
func (r Result) Duration() time.Duration {
	if r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
		return 0
	}
	return r.FinishedAt.Sub(r.StartedAt)
}

// stepEnv is the full environment of a workload step. Nothing from the
// worker's own environment leaks in. Caches point at /tmp because the root
// filesystem is read-only.
func stepEnv(o Owner) []string {
	return []string{
		"HOME=/tmp",
		"TMPDIR=/tmp",
		"CI=true",
		"GOPATH=/tmp/go",
		"GOCACHE=/tmp/go-build",
		"npm_config_cache=/tmp/.npm",
		"PIP_CACHE_DIR=/tmp/.cache/pip",
		"TSUZUKU_JOB_ID=" + o.JobID.String(),
		"TSUZUKU_ATTEMPT_ID=" + o.AttemptID.String(),
	}
}

func checkoutEnv(repo, rev string) []string {
	return []string{
		"HOME=/tmp",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"TSZ_REPO=" + repo,
		"TSZ_REV=" + rev,
	}
}

func checkoutStep(image string, timeout time.Duration) Step {
	return Step{
		Role:      workerapi.RolePrepare,
		Image:     image,
		Command:   checkoutScript,
		CPUMillis: checkoutCPUMillis,
		MemoryMB:  checkoutMemoryMB,
		Network:   true,
		Timeout:   timeout,
	}
}

// containerSpec builds the locked-down container for a step: no capabilities,
// no privilege escalation, a read-only root filesystem with a size-capped /tmp,
// hard CPU, memory, and process limits, and no network unless the step asks.
func containerSpec(ws Workspace, s Step, env []string) (*container.Config, *container.HostConfig) {
	memory := int64(s.MemoryMB) << 20
	pids := int64(pidsLimit)
	initProcess := true
	network := container.NetworkMode("bridge")
	if !s.Network {
		network = "none"
	}
	cfg := &container.Config{
		Image:      s.Image,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{s.Command},
		WorkingDir: WorkspaceDir,
		Env:        env,
		Labels:     ws.Owner.labels(s.Role),
	}
	host := &container.HostConfig{
		NetworkMode:    network,
		Mounts:         []mount.Mount{{Type: mount.TypeVolume, Source: ws.Volume, Target: WorkspaceDir}},
		Tmpfs:          map[string]string{"/tmp": fmt.Sprintf("rw,exec,nosuid,nodev,size=%dm", s.MemoryMB)},
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges:true"},
		Init:           &initProcess,
		Resources: container.Resources{
			Memory:     memory,
			MemorySwap: memory,
			NanoCPUs:   int64(s.CPUMillis) * 1_000_000,
			PidsLimit:  &pids,
		},
	}
	return cfg, host
}

// TailBuffer keeps only the last limit bytes written to it. It is not safe
// for concurrent writes.
type TailBuffer struct {
	limit int
	buf   []byte
}

// NewTailBuffer returns a TailBuffer that keeps the last limit bytes.
func NewTailBuffer(limit int) *TailBuffer {
	return &TailBuffer{limit: limit}
}

func (t *TailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

// String returns the kept bytes without surrounding whitespace.
func (t *TailBuffer) String() string {
	return string(bytes.TrimSpace(t.buf))
}

// lastLine returns the last non-empty line of out.
func lastLine(out []byte) string {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	return string(bytes.TrimSpace(lines[len(lines)-1]))
}

func isCommitHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

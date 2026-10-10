package runtime

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/mount"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

func testWorkspace() Workspace {
	owner := Owner{Worker: "worker-01", JobID: uuid.New(), AttemptID: uuid.New()}
	return Workspace{Volume: workspaceName(owner.AttemptID), Owner: owner}
}

func TestContainerSpecLocksDownTheContainer(t *testing.T) {
	ws := testWorkspace()
	step := Step{Role: workerapi.RoleExecute, Image: "golang:1.27", Command: "go test ./...", CPUMillis: 1500, MemoryMB: 768}

	cfg, host := containerSpec(ws, step, stepEnv(ws.Owner))

	if cfg.Image != "golang:1.27" || cfg.WorkingDir != WorkspaceDir {
		t.Errorf("image/workdir = %q/%q", cfg.Image, cfg.WorkingDir)
	}
	if !slices.Equal(cfg.Entrypoint, []string{"/bin/sh", "-c"}) || !slices.Equal(cfg.Cmd, []string{"go test ./..."}) {
		t.Errorf("entrypoint/cmd = %v/%v", cfg.Entrypoint, cfg.Cmd)
	}
	if host.NetworkMode != "none" {
		t.Errorf("network = %q, want none when the step does not ask for network", host.NetworkMode)
	}
	if !host.ReadonlyRootfs {
		t.Error("root filesystem is writable")
	}
	if !slices.Equal(host.CapDrop, []string{"ALL"}) {
		t.Errorf("cap drop = %v", host.CapDrop)
	}
	if !slices.Contains(host.SecurityOpt, "no-new-privileges:true") {
		t.Errorf("security opts = %v", host.SecurityOpt)
	}
	if host.Privileged {
		t.Error("container is privileged")
	}
	if host.Init == nil || !*host.Init {
		t.Error("init process disabled; zombie processes would not be reaped")
	}
	if got, want := host.Memory, int64(768)<<20; got != want {
		t.Errorf("memory = %d, want %d", got, want)
	}
	if host.MemorySwap != host.Memory {
		t.Errorf("memory swap = %d, want equal to memory so the step cannot swap", host.MemorySwap)
	}
	if host.NanoCPUs != 1_500_000_000 {
		t.Errorf("nano cpus = %d", host.NanoCPUs)
	}
	if host.PidsLimit == nil || *host.PidsLimit != pidsLimit {
		t.Errorf("pids limit = %v", host.PidsLimit)
	}
	if got := host.Tmpfs["/tmp"]; got != "rw,exec,nosuid,nodev,size=768m" {
		t.Errorf("tmpfs = %q", got)
	}
	want := mount.Mount{Type: mount.TypeVolume, Source: ws.Volume, Target: WorkspaceDir}
	if len(host.Mounts) != 1 || host.Mounts[0] != want {
		t.Errorf("mounts = %+v", host.Mounts)
	}
	if len(host.Binds) != 0 {
		t.Errorf("bind mounts = %v; a step must never see the host filesystem", host.Binds)
	}
}

func TestContainerSpecNetworkOptIn(t *testing.T) {
	ws := testWorkspace()
	_, host := containerSpec(ws, Step{Role: workerapi.RoleExecute, Image: "alpine", CPUMillis: 100, MemoryMB: 64, Network: true}, nil)
	if host.NetworkMode != "bridge" {
		t.Errorf("network = %q, want bridge", host.NetworkMode)
	}
}

func TestContainerSpecLabels(t *testing.T) {
	ws := testWorkspace()
	cfg, _ := containerSpec(ws, Step{Role: workerapi.RoleVerify, Image: "alpine", CPUMillis: 100, MemoryMB: 64}, nil)
	want := map[string]string{
		LabelManaged: "true",
		LabelWorker:  "worker-01",
		LabelJob:     ws.Owner.JobID.String(),
		LabelAttempt: ws.Owner.AttemptID.String(),
		LabelRole:    workerapi.RoleVerify,
	}
	for k, v := range want {
		if cfg.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, cfg.Labels[k], v)
		}
	}
}

func TestStepEnvDoesNotLeakWorkerEnvironment(t *testing.T) {
	t.Setenv("TSUZUKU_WORKER_TOKEN", "secret")
	ws := testWorkspace()
	for _, kv := range stepEnv(ws.Owner) {
		if strings.Contains(kv, "secret") || strings.HasPrefix(kv, "TSUZUKU_WORKER_") {
			t.Errorf("step env contains worker setting %q", kv)
		}
	}
	if !slices.Contains(stepEnv(ws.Owner), "TSUZUKU_ATTEMPT_ID="+ws.Owner.AttemptID.String()) {
		t.Error("step env is missing the attempt id")
	}
}

func TestCheckoutNeverInterpolatesUserInput(t *testing.T) {
	repo := "https://example.com/a.git; rm -rf /"
	rev := "$(touch /pwned)"
	step := checkoutStep("alpine/git:v2.49.1", time.Minute)
	if strings.Contains(step.Command, repo) || strings.Contains(step.Command, rev) {
		t.Fatal("checkout script contains user input")
	}
	env := checkoutEnv(repo, rev)
	if !slices.Contains(env, "TSZ_REPO="+repo) || !slices.Contains(env, "TSZ_REV="+rev) {
		t.Errorf("checkout env = %v", env)
	}
	if !step.Network || step.Role != workerapi.RolePrepare {
		t.Errorf("checkout step = %+v", step)
	}
}

func TestNames(t *testing.T) {
	ws := testWorkspace()
	id := ws.Owner.AttemptID.String()
	if ws.Volume != "tsuzuku-"+id {
		t.Errorf("volume = %q", ws.Volume)
	}
	if got := containerName(ws, workerapi.RoleExecute); got != "tsuzuku-"+id+"-execute" {
		t.Errorf("container = %q", got)
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	b := &tailBuffer{limit: 5}
	_, _ = b.Write([]byte("abc"))
	_, _ = b.Write([]byte("defgh"))
	if got := b.String(); got != "defgh" {
		t.Errorf("tail = %q", got)
	}
}

func TestLastLine(t *testing.T) {
	cases := map[string]string{
		"abc\n":            "abc",
		"warning\nabc\n\n": "abc",
		"":                 "",
		"  x  ":            "x",
	}
	for in, want := range cases {
		if got := lastLine([]byte(in)); got != want {
			t.Errorf("lastLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsCommitHash(t *testing.T) {
	cases := map[string]bool{
		strings.Repeat("a", 40): true,
		strings.Repeat("0", 64): true,
		strings.Repeat("A", 40): false,
		strings.Repeat("g", 40): false,
		strings.Repeat("a", 39): false,
		"":                      false,
	}
	for in, want := range cases {
		if got := isCommitHash(in); got != want {
			t.Errorf("isCommitHash(%q) = %v", in, got)
		}
	}
}

func TestResultDuration(t *testing.T) {
	start := time.Now()
	if d := (Result{StartedAt: start, FinishedAt: start.Add(2 * time.Second)}).Duration(); d != 2*time.Second {
		t.Errorf("duration = %s", d)
	}
	if d := (Result{}).Duration(); d != 0 {
		t.Errorf("zero result duration = %s", d)
	}
}

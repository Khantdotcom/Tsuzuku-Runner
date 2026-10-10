//go:build integration

package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

const (
	testImage    = "alpine:3.22"
	testGitImage = "alpine/git:v2.49.1"
)

func newTestDocker(t *testing.T) *Docker {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := NewDocker(ctx, testGitImage, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("docker: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.EnsureImage(ctx, testImage); err != nil {
		t.Fatalf("pull %s: %v", testImage, err)
	}
	return d
}

func newTestWorkspace(t *testing.T, d *Docker) Workspace {
	t.Helper()
	owner := Owner{Worker: "test-" + uuid.NewString()[:8], JobID: uuid.New(), AttemptID: uuid.New()}
	ws, err := d.CreateWorkspace(context.Background(), owner)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := d.RemoveWorkspace(context.Background(), ws); err != nil {
			t.Errorf("remove workspace: %v", err)
		}
	})
	return ws
}

func shellStep(role, cmd string) Step {
	return Step{Role: role, Image: testImage, Command: cmd, CPUMillis: 500, MemoryMB: 128, Timeout: 30 * time.Second}
}

func runStep(t *testing.T, d *Docker, ws Workspace, step Step) (Result, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	res, err := d.Run(context.Background(), ws, step, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run %q: %v", step.Command, err)
	}
	return res, stdout.String(), stderr.String()
}

func TestDockerRunCapturesOutputAndExitCode(t *testing.T) {
	d := newTestDocker(t)
	ws := newTestWorkspace(t, d)

	res, stdout, stderr := runStep(t, d, ws, shellStep(workerapi.RoleExecute, "echo out; echo err >&2; exit 3"))

	if res.ExitCode != 3 || res.TimedOut {
		t.Errorf("exit = %d timed out = %v", res.ExitCode, res.TimedOut)
	}
	if stdout != "out\n" || stderr != "err\n" {
		t.Errorf("stdout = %q stderr = %q", stdout, stderr)
	}
	if res.ContainerID == "" || res.Duration() <= 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestDockerRunIsolation(t *testing.T) {
	d := newTestDocker(t)
	ws := newTestWorkspace(t, d)

	script := strings.Join([]string{
		`touch /etc/x 2>/dev/null && echo rootfs=writable || echo rootfs=readonly`,
		`echo hi > /workspace/f && echo workspace=writable`,
		`echo hi > /tmp/f && echo tmp=writable`,
		`chown 1234 /workspace/f 2>/dev/null && echo chown=allowed || echo chown=denied`,
		`wget -q -T 3 -O /dev/null http://1.1.1.1 2>/dev/null && echo network=on || echo network=off`,
		`env | grep -c TSUZUKU_WORKER || true`,
	}, "\n")
	res, stdout, _ := runStep(t, d, ws, shellStep(workerapi.RoleExecute, script))
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, stdout = %s", res.ExitCode, stdout)
	}
	for _, want := range []string{"rootfs=readonly", "workspace=writable", "tmp=writable", "chown=denied", "network=off", "\n0\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

func TestDockerWorkspaceIsSharedBetweenSteps(t *testing.T) {
	d := newTestDocker(t)
	ws := newTestWorkspace(t, d)

	runStep(t, d, ws, shellStep(workerapi.RoleExecute, "echo built > /workspace/out.txt"))
	_, stdout, _ := runStep(t, d, ws, shellStep(workerapi.RoleVerify, "cat out.txt"))
	if stdout != "built\n" {
		t.Errorf("verify step saw %q", stdout)
	}
}

func TestDockerRunTimeoutKillsTheContainer(t *testing.T) {
	d := newTestDocker(t)
	ws := newTestWorkspace(t, d)

	step := shellStep(workerapi.RoleExecute, "echo started; sleep 60")
	step.Timeout = time.Second
	start := time.Now()
	res, stdout, _ := runStep(t, d, ws, step)

	if !res.TimedOut {
		t.Error("step did not time out")
	}
	if res.ExitCode == 0 {
		t.Error("killed step reported exit code 0")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("timeout took %s", elapsed)
	}
	if stdout != "started\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestDockerRunCancelKillsTheContainer(t *testing.T) {
	d := newTestDocker(t)
	ws := newTestWorkspace(t, d)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := d.Run(ctx, ws, shellStep(workerapi.RoleExecute, "sleep 60"), io.Discard, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline", err)
	}
	assertNoContainers(t, d, ws.Owner.Worker)
}

func TestDockerRunCancelledWhileStartingLeavesNothing(t *testing.T) {
	d := newTestDocker(t)
	for _, delay := range []time.Duration{0, 20 * time.Millisecond, 100 * time.Millisecond, 300 * time.Millisecond} {
		ws := newTestWorkspace(t, d)
		ctx, cancel := context.WithTimeout(context.Background(), delay)
		_, err := d.Run(ctx, ws, shellStep(workerapi.RoleExecute, "sleep 60"), io.Discard, io.Discard)
		cancel()
		if err == nil {
			t.Fatalf("delay %s: Run succeeded, want a cancellation error", delay)
		}
		assertNoContainers(t, d, ws.Owner.Worker)
		if err := d.RemoveWorkspace(context.Background(), ws); err != nil {
			t.Errorf("delay %s: remove workspace: %v", delay, err)
		}
	}
}

func TestDockerRemoveWorkspaceInUse(t *testing.T) {
	d := newTestDocker(t)
	ws := newTestWorkspace(t, d)
	ctx := context.Background()

	cfg, host := containerSpec(ws, shellStep(workerapi.RoleExecute, "sleep 60"), stepEnv(ws.Owner))
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "tsuzuku-inuse-" + uuid.NewString()[:8], Config: cfg, HostConfig: host})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		d.removeContainer(ctx, created.ID)
		t.Fatal(err)
	}

	if err := d.RemoveWorkspace(ctx, ws); err != nil {
		t.Fatalf("remove workspace held by a running container: %v", err)
	}
	assertNoContainers(t, d, ws.Owner.Worker)
}

func TestDockerCheckout(t *testing.T) {
	d := newTestDocker(t)
	ws := newTestWorkspace(t, d)

	commit, res, err := d.Checkout(context.Background(), ws, "https://github.com/octocat/Hello-World", "master", 2*time.Minute)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if !isCommitHash(commit) || res.Step.Role != workerapi.RolePrepare || res.Step.Image != testGitImage {
		t.Errorf("commit = %q result = %+v", commit, res)
	}

	_, stdout, _ := runStep(t, d, ws, shellStep(workerapi.RoleExecute, "ls"))
	if !strings.Contains(stdout, "README") {
		t.Errorf("workspace contents = %q", stdout)
	}

	again := newTestWorkspace(t, d)
	byHash, _, err := d.Checkout(context.Background(), again, "https://github.com/octocat/Hello-World", commit, 2*time.Minute)
	if err != nil || byHash != commit {
		t.Errorf("checkout by hash = %q, %v; want %q", byHash, err, commit)
	}
}

func TestDockerCheckoutUnknownRevision(t *testing.T) {
	d := newTestDocker(t)
	ws := newTestWorkspace(t, d)

	_, _, err := d.Checkout(context.Background(), ws, "https://github.com/octocat/Hello-World", "no-such-branch-tsuzuku", 2*time.Minute)
	if !errors.Is(err, ErrCheckoutFailed) {
		t.Fatalf("err = %v, want ErrCheckoutFailed", err)
	}
}

func TestDockerCleanupLeftovers(t *testing.T) {
	d := newTestDocker(t)
	ctx := context.Background()
	mine := Owner{Worker: "test-" + uuid.NewString()[:8], JobID: uuid.New(), AttemptID: uuid.New()}
	other := Owner{Worker: "test-" + uuid.NewString()[:8], JobID: uuid.New(), AttemptID: uuid.New()}

	ws, err := d.CreateWorkspace(ctx, mine)
	if err != nil {
		t.Fatal(err)
	}
	otherWS, err := d.CreateWorkspace(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.RemoveWorkspace(ctx, otherWS) })

	cfg, host := containerSpec(ws, shellStep(workerapi.RoleExecute, "sleep 60"), nil)
	if _, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: containerName(ws, workerapi.RoleExecute), Config: cfg, HostConfig: host}); err != nil {
		t.Fatal(err)
	}

	removed, err := d.CleanupLeftovers(ctx, mine.Worker)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want the container and the volume", removed)
	}
	assertNoContainers(t, d, mine.Worker)
	if _, err := d.cli.VolumeInspect(ctx, otherWS.Volume, client.VolumeInspectOptions{}); err != nil {
		t.Errorf("another worker's volume was removed: %v", err)
	}
}

func assertNoContainers(t *testing.T, d *Docker, worker string) {
	t.Helper()
	filters := make(client.Filters).Add("label", LabelWorker+"="+worker)
	list, err := d.cli.ContainerList(context.Background(), client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("%d containers left behind", len(list.Items))
	}
}

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// cleanupTimeout bounds API calls that must still run after the caller's
// context is cancelled, such as killing and removing a container.
const cleanupTimeout = 30 * time.Second

// Docker runs steps as containers on a Docker daemon.
type Docker struct {
	cli      *client.Client
	gitImage string
	logger   *slog.Logger
}

// NewDocker connects to the daemon described by the standard DOCKER_HOST
// environment variables and checks that it answers.
func NewDocker(ctx context.Context, gitImage string, logger *slog.Logger) (*Docker, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	if _, err := cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("reach docker daemon: %w", err)
	}
	return &Docker{cli: cli, gitImage: gitImage, logger: logger}, nil
}

// Close releases the client's connections.
func (d *Docker) Close() error {
	return d.cli.Close()
}

// EnsureImage pulls image unless the daemon already has it.
func (d *Docker) EnsureImage(ctx context.Context, image string) error {
	if _, err := d.cli.ImageInspect(ctx, image); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image %s: %w", image, err)
	}
	d.logger.InfoContext(ctx, "pulling image", "image", image)
	resp, err := d.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}
	defer func() { _ = resp.Close() }()
	if err := resp.Wait(ctx); err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}
	return nil
}

// CreateWorkspace creates an empty volume for one attempt.
func (d *Docker) CreateWorkspace(ctx context.Context, owner Owner) (Workspace, error) {
	ws := Workspace{Volume: workspaceName(owner.AttemptID), Owner: owner}
	_, err := d.cli.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name:   ws.Volume,
		Labels: owner.labels("workspace"),
	})
	if err != nil {
		return Workspace{}, fmt.Errorf("create workspace volume: %w", err)
	}
	return ws, nil
}

// RemoveWorkspace deletes the workspace volume. It still runs if ctx is
// already cancelled, because leaking volumes fills the disk.
func (d *Docker) RemoveWorkspace(ctx context.Context, ws Workspace) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	_, err := d.cli.VolumeRemove(ctx, ws.Volume, client.VolumeRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove workspace volume: %w", err)
	}
	return nil
}

// Checkout clones repo at rev into the workspace using the git helper image.
func (d *Docker) Checkout(ctx context.Context, ws Workspace, repo, rev string, timeout time.Duration) (string, Result, error) {
	if err := d.EnsureImage(ctx, d.gitImage); err != nil {
		return "", Result{}, err
	}
	step := checkoutStep(d.gitImage, timeout)
	stdout := &tailBuffer{limit: tailBytes}
	stderr := &tailBuffer{limit: tailBytes}
	cfg, host := containerSpec(ws, step, checkoutEnv(repo, rev))
	res, err := d.run(ctx, containerName(ws, step.Role), step, cfg, host, stdout, stderr)
	if err != nil {
		return "", res, err
	}
	switch {
	case res.TimedOut:
		return "", res, fmt.Errorf("%w: timed out after %s", ErrCheckoutFailed, timeout)
	case res.ExitCode != 0:
		return "", res, fmt.Errorf("%w: git exited with code %d: %s", ErrCheckoutFailed, res.ExitCode, stderr.String())
	}
	commit := lastLine(stdout.buf)
	if !isCommitHash(commit) {
		return "", res, fmt.Errorf("%w: unexpected git output %q", ErrCheckoutFailed, commit)
	}
	return commit, res, nil
}

// Run executes step in the workspace, streaming its output to stdout and stderr.
func (d *Docker) Run(ctx context.Context, ws Workspace, step Step, stdout, stderr io.Writer) (Result, error) {
	cfg, host := containerSpec(ws, step, stepEnv(ws.Owner))
	return d.run(ctx, containerName(ws, step.Role), step, cfg, host, stdout, stderr)
}

func (d *Docker) run(ctx context.Context, name string, step Step, cfg *container.Config, host *container.HostConfig, stdout, stderr io.Writer) (Result, error) {
	res := Result{Step: step}
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: cfg, HostConfig: host})
	if err != nil {
		return res, fmt.Errorf("create %s container: %w", step.Role, err)
	}
	res.ContainerID = created.ID
	defer d.removeContainer(ctx, created.ID)

	// Waiting and log streaming outlive ctx so a cancelled step can still be
	// killed and its exit observed.
	bg, stopBG := context.WithCancel(context.WithoutCancel(ctx))
	defer stopBG()
	wait := d.cli.ContainerWait(bg, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})

	res.StartedAt = time.Now()
	if _, err := d.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return res, fmt.Errorf("start %s container: %w", step.Role, err)
	}

	logs, err := d.cli.ContainerLogs(bg, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true})
	if err != nil {
		d.kill(ctx, created.ID)
		return res, fmt.Errorf("attach to %s container logs: %w", step.Role, err)
	}
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		defer func() { _ = logs.Close() }()
		if _, err := stdcopy.StdCopy(stdout, stderr, logs); err != nil && bg.Err() == nil {
			d.logger.WarnContext(bg, "copy container logs", "container_id", created.ID, "err", err)
		}
	}()

	timer := time.NewTimer(step.Timeout)
	defer timer.Stop()
	var exit container.WaitResponse
	select {
	case exit = <-wait.Result:
	case err := <-wait.Error:
		d.kill(ctx, created.ID)
		return res, fmt.Errorf("wait for %s container: %w", step.Role, err)
	case <-timer.C:
		res.TimedOut = true
		d.kill(ctx, created.ID)
		if exit, err = awaitExit(wait); err != nil {
			return res, fmt.Errorf("wait for killed %s container: %w", step.Role, err)
		}
	case <-ctx.Done():
		d.kill(ctx, created.ID)
		_, _ = awaitExit(wait)
		return res, ctx.Err()
	}
	res.FinishedAt = time.Now()
	res.ExitCode = int(exit.StatusCode)

	// The log stream ends when the container exits; give it a moment to drain.
	select {
	case <-copied:
	case <-time.After(5 * time.Second):
		d.logger.WarnContext(ctx, "container logs did not drain", "container_id", created.ID)
	}
	if exit.Error != nil && exit.Error.Message != "" {
		return res, fmt.Errorf("%s container: %s", step.Role, exit.Error.Message)
	}
	return res, nil
}

func awaitExit(wait client.ContainerWaitResult) (container.WaitResponse, error) {
	select {
	case exit := <-wait.Result:
		return exit, nil
	case err := <-wait.Error:
		return container.WaitResponse{}, err
	case <-time.After(cleanupTimeout):
		return container.WaitResponse{}, errors.New("container did not exit after kill")
	}
}

func (d *Docker) kill(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if _, err := d.cli.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: "KILL"}); err != nil &&
		!cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
		d.logger.WarnContext(ctx, "kill container", "container_id", id, "err", err)
	}
}

func (d *Docker) removeContainer(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if _, err := d.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil &&
		!cerrdefs.IsNotFound(err) {
		d.logger.WarnContext(ctx, "remove container", "container_id", id, "err", err)
	}
}

// CleanupLeftovers removes containers and workspaces that this worker created
// before a crash or forced restart. Other workers sharing the daemon are left
// alone.
func (d *Docker) CleanupLeftovers(ctx context.Context, worker string) (int, error) {
	filters := make(client.Filters).Add("label", LabelManaged+"=true").Add("label", LabelWorker+"="+worker)
	containers, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return 0, fmt.Errorf("list leftover containers: %w", err)
	}
	removed := 0
	for _, c := range containers.Items {
		d.removeContainer(ctx, c.ID)
		removed++
	}
	volumes, err := d.cli.VolumeList(ctx, client.VolumeListOptions{Filters: filters})
	if err != nil {
		return removed, fmt.Errorf("list leftover volumes: %w", err)
	}
	for _, v := range volumes.Items {
		if err := d.RemoveWorkspace(ctx, Workspace{Volume: v.Name}); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

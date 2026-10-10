# ADR 0005: Run workloads in locked-down Docker containers on a per-attempt volume

- **Status:** Accepted
- **Date:** 2026-10-11

## Context

A worker runs arbitrary commands from submitted workloads against arbitrary public repositories. The command must not be able to reach the worker's credentials, the host filesystem, other jobs, or (unless the workload asks) the network, and it must not be able to exhaust the host. A job also has several steps: check out the repository, run the command, and later run verification, so steps need a shared place to keep files.

Options considered:

- **Run commands directly on the worker host.** Simplest, but no isolation at all.
- **One long-lived container per job** with steps run through `docker exec`. Fewer containers, but each step inherits whatever the previous step left running, and per-step limits and timeouts are awkward.
- **One container per step, sharing a named volume per attempt.** Each step starts clean and gets its own limits and timeout; the volume carries the checkout from step to step.
- **A stronger sandbox** (gVisor, Firecracker, Kata). Better isolation, but extra host setup that the MVP does not need. It can replace the Docker runtime behind the same interface later.

## Decision

Run each step in its own Docker container, sharing one named volume per attempt.

- `internal/runtime` defines a small `Runtime` interface (pull image, create workspace, check out, run step, remove workspace). The worker's executor only uses the interface, so it is unit-tested with a fake. `runtime.Docker` implements it with the Docker Engine API (`github.com/moby/moby/client`).
- The workspace is a named volume, `tsuzuku-<attempt id>`, mounted at `/workspace`. It is removed when the attempt ends, whatever the outcome.
- The checkout runs in a pinned git helper image (`alpine/git`, configurable with `TSUZUKU_WORKER_GIT_IMAGE`). The repository URL and revision are passed as environment variables, never interpolated into the shell script, so a crafted revision cannot inject commands. It tries a shallow fetch of the revision first and falls back to fetching all branches and tags. The resolved commit hash is reported to the server.
- Every step container is locked down:
  - all Linux capabilities dropped and `no-new-privileges` set;
  - read-only root filesystem, with a writable `tmpfs` at `/tmp` capped at the step's memory limit;
  - memory limit with swap disabled, a CPU quota, and a limit of 512 processes;
  - an init process to reap zombies;
  - no network unless the workload sets `runtime.network`;
  - a fixed environment (`HOME=/tmp`, `CI=true`, tool caches under `/tmp`), so nothing from the worker's environment, including its token, leaks in;
  - no bind mounts of host paths.
- A step that runs past its timeout is killed and reported as timed out. A step whose context is cancelled is killed; the attempt is reported as cancelled when the server asked for it, and as failed when the worker is shutting down.
- Creating and starting a container are never interrupted part-way. An aborted create request can still complete inside the daemon, leaving a container the worker never learned about, so both calls run to completion and the step checks for cancellation right after. Removing a workspace first force-removes any container still using the volume, then retries briefly while the daemon releases it.
- Containers and volumes carry labels (`dev.tsuzuku.managed`, `dev.tsuzuku.worker`, job, attempt, role). On startup a worker removes anything labelled with its own name, left over from a crash. Workers sharing a daemon never touch each other's resources.
- The worker reports facts (commit, exit code, timed out, durations, container details); the server decides the job's outcome. Reports are fenced: the attempt must belong to the reporting worker and still be running, so a stale or misdirected report is rejected and changes nothing.

## Consequences

- Isolation depends on the Docker daemon and the host kernel. Containers share the kernel, so this is not a boundary against kernel exploits; a stronger sandbox can be added behind `Runtime`.
- The worker needs the Docker socket. Access to the socket is equivalent to root on the host, so in Compose the worker runs as root with the socket mounted, and the worker must be treated as a trusted, privileged process. Workload containers are unprivileged.
- Steps run the image's default user, usually root inside the container. Without capabilities and with a read-only root filesystem that user cannot change ownership, load modules, or write outside `/workspace` and `/tmp`.
- Workload images must contain `/bin/sh`, because commands run through `sh -c`. The image policy (Milestone 1) only allows official `golang`, `node`, and `python` images, which all do.
- Each step pays container start-up time, about a second. That is small next to checkout and build times.
- Images are pulled on first use and kept; the daemon's own garbage collection decides when to remove them.

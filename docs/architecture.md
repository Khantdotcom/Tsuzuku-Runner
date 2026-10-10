# Architecture

Tsuzuku Runner is an execution platform for autonomous software workloads. Its job is to make execution **continue safely** after failures: persist every workload durably, run it in an isolated runtime, verify the result independently of the executor, and keep enough evidence to explain every outcome.

## System overview

```mermaid
flowchart TD
    UI[Web UI] -->|REST / SSE| API[Go API server]
    API --> DB[(PostgreSQL)]
    API --> SCH[Scheduler]
    SCH --> DB
    W1[Worker] -->|HTTP| API
    W2[Worker] -->|HTTP| API
    W1 --> RT1[Docker runtime]
    W2 --> RT2[Docker runtime]
    RT1 --> VER[Verification]
    RT2 --> VER
    VER --> EV[Evidence]
```

| Component    | Responsibility                                                                 |
| ------------ | ------------------------------------------------------------------------------ |
| API server   | Workload intake, job queries, worker coordination endpoints, log streaming     |
| PostgreSQL   | Durable source of truth for workloads, jobs, attempts, transitions, and events |
| Scheduler    | Places queued jobs on workers; runs inside the API server process              |
| Worker       | Claims assigned jobs, drives the runtime, reports logs and results             |
| Runtime      | Isolated execution environment (Docker for the MVP)                            |
| Verification | Independent checks that decide whether a job succeeded                         |
| Evidence     | Stored record that explains every final decision                               |

Workers talk to the control plane only through the HTTP API and never hold database credentials.

## Process layout

The codebase is a modular monolith ([ADR 0001](adr/0001-modular-monolith.md)) with two long-running binaries and two one-shot tools:

| Binary    | Source        | Role                                           |
| --------- | ------------- | ---------------------------------------------- |
| `server`  | `cmd/server`  | API server and scheduler                       |
| `worker`  | `cmd/worker`  | Job execution on a host with Docker            |
| `migrate` | `cmd/migrate` | Apply, roll back, or inspect schema migrations |
| `seed`    | `cmd/seed`    | Load example job history for development       |

The dashboard is a separate Next.js app in `frontend/`.

Domain packages live under `internal/`, one package per bounded area (`api`, `job`, `scheduler`, `worker`, `runtime`, `verification`, `evidence`, ...). Packages are added as they gain real code.

## Configuration and logging

- Configuration is read from `TSUZUKU_*` environment variables into typed structs (`internal/config`) and validated at startup; invalid configuration fails fast.
- Logging uses `log/slog`: human-readable text in development, JSON in every other environment. Every record carries a `service` attribute (`server` or `worker`).
- Both processes shut down gracefully on `SIGINT`/`SIGTERM`.

## HTTP API

Errors use RFC 9457 problem details (`application/problem+json`).

| Endpoint                              | Auth         | Purpose                                                    |
| ------------------------------------- | ------------ | ---------------------------------------------------------- |
| `GET /healthz`                        | none         | Liveness: the process is up                                |
| `GET /readyz`                         | none         | Readiness: the database answers a ping within 2s           |
| `POST /api/v1/workers/register`       | worker token | Register or re-register a worker; returns its ID           |
| `POST /api/v1/workers/{id}/heartbeat` | worker token | Report liveness and host usage; `404` if the ID is unknown |
| `POST /api/v1/workers/{id}/claim`     | worker token | Start the worker's next assigned job; long-polls with `?wait=` |
| `GET /api/v1/workers`                 | none         | List workers with capacity, usage, and online status       |
| `POST /api/v1/workloads`              | none         | Submit a workload; creates a `QUEUED` job                  |
| `GET /api/v1/jobs`                    | none         | List jobs newest first (`state`, `limit`, `before` cursor) |
| `GET /api/v1/jobs/{id}`               | none         | Job with its workload and state history                    |
| `GET /api/v1/jobs/{id}/attempts`      | none         | Attempts in order                                          |
| `GET /api/v1/jobs/{id}/events`        | none         | Timeline events after an `after` cursor                    |
| `GET /api/v1/jobs/{id}/logs`          | none         | Log chunks after an `after` cursor (data is base64)        |

Workers authenticate with a shared bearer token (`TSUZUKU_WORKER_TOKEN`), compared in constant time. Request and response types for the worker endpoints live in `internal/workerapi`, which both binaries import.

`{id}` is a job UUID or its number, so `/api/v1/jobs/42` works. The submission and job endpoints have no authentication yet; every port binds to localhost only, and client authentication is a later milestone.

## Workload submission

`POST /api/v1/workloads` takes a JSON body:

| Field                       | Required | Default        | Rules                                                          |
| --------------------------- | -------- | -------------- | -------------------------------------------------------------- |
| `repository`                | yes      |                | `https://` URL without credentials, query, or fragment         |
| `revision`                  | yes      |                | Branch, tag, or commit                                         |
| `command`                   | yes      |                | Up to 4096 characters                                          |
| `acceptance_criteria`       | no       | `[]`           | Up to 20 entries of up to 500 characters                       |
| `resources.cpu`             | no       | 1 core         | 0.1 core up to `TSUZUKU_WORKLOAD_MAX_CPU_MILLIS`               |
| `resources.memory_mb`       | no       | 1024           | 64 up to `TSUZUKU_WORKLOAD_MAX_MEMORY_MB`                      |
| `timeout_seconds`           | no       | 600            | 1 up to `TSUZUKU_WORKLOAD_MAX_TIMEOUT`                         |
| `runtime.image`             | no       | `golang:1.27`  | Official `golang`, `node`, or `python` image; no tag means `latest` |
| `runtime.network`           | no       | `true`         |                                                                |
| `verification.command`      | no       |                | Run after `command` when given                                 |

Defaults above a configured maximum are lowered to it. Unknown fields and wrong types are rejected with `400`; values that break a rule return `422` with the field named in `detail`. The normalized spec, with every default filled in, is stored as `workloads.spec`.

A successful submission returns `201 Created`, a `Location` header, and the job. An optional `Idempotency-Key` header (1–255 visible ASCII characters) makes retries safe: the same key with the same normalized spec returns the original job with `200 OK` and `Idempotent-Replayed: true`; the same key with a different spec returns `409 Conflict`.

## Job lifecycle

```mermaid
stateDiagram-v2
    [*] --> QUEUED: submitted
    QUEUED --> SCHEDULED
    SCHEDULED --> PREPARING
    PREPARING --> EXECUTING
    EXECUTING --> VERIFYING
    VERIFYING --> COMPLETED
    PREPARING --> FAILED
    EXECUTING --> FAILED
    VERIFYING --> FAILED
    QUEUED --> CANCELLED
    SCHEDULED --> CANCELLED
    PREPARING --> CANCELLED
    EXECUTING --> CANCELLED
    VERIFYING --> CANCELLED
    COMPLETED --> [*]
    FAILED --> [*]
    CANCELLED --> [*]
```

`internal/job` owns the table of legal transitions; every change goes through `job.Transition`, which applies a compare-and-set update and records the history row and a timeline event in the same transaction ([ADR 0004](adr/0004-job-state-machine.md)). `RETRYING`, `REPAIRING`, and `BLOCKED` exist in the schema but have no edges until the milestones that use them.

## Scheduling and claiming

Placement and starting are separate steps ([ADR 0003](adr/0003-postgres-as-job-queue.md)): the scheduler decides where a job runs, and the assigned worker claims it when it is ready.

```mermaid
sequenceDiagram
    participant C as Client
    participant A as API server
    participant S as Scheduler
    participant DB as PostgreSQL
    participant W as Worker
    C->>A: POST /workloads
    A->>DB: workload + job (QUEUED)
    W->>A: POST /workers/{id}/claim?wait=25s
    Note over A,W: request held open
    loop every TSUZUKU_SCHEDULER_INTERVAL
        S->>DB: advisory lock, online workers, oldest QUEUED jobs (SKIP LOCKED)
        S->>DB: QUEUED to SCHEDULED, assigned_worker_id set
    end
    S-->>A: wake waiting claims
    A->>DB: lock assigned job, create attempt, SCHEDULED to PREPARING
    A-->>W: 200 job, attempt, workload spec
```

**Scheduler.** One loop inside `server` runs a round every `TSUZUKU_SCHEDULER_INTERVAL` (1s by default). Each round is one transaction:

1. Take the advisory lock with `pg_try_advisory_xact_lock`. If another server replica holds it, skip the round. The lock is released at commit, so a crashed replica never leaves it held.
2. Load online workers (same database-clock rule as `/workers`) and count their unfinished jobs (`SCHEDULED` through `VERIFYING`).
3. Lock up to 100 of the oldest placeable `QUEUED` jobs (`scheduled_at` empty or past) with `FOR UPDATE SKIP LOCKED`.
4. Place each job, oldest first, on the worker with the fewest unfinished jobs that still has a free slot; ties go to the earliest-registered worker.
5. Move each placed job to `SCHEDULED` with its worker, recording a reason such as `placed on worker-01 (0 of 2 slots busy)`.

The advisory lock matters even with `SKIP LOCKED`: two rounds running at once would each see the same free slots and lock different jobs, overfilling the worker. Placement counts slots only; CPU- and memory-aware placement comes later.

**Claiming.** `POST /api/v1/workers/{id}/claim` locks the worker's oldest `SCHEDULED` job with `FOR UPDATE SKIP LOCKED`, creates the next `job_attempts` row, and moves the job to `PREPARING` in one transaction, so concurrent claims never start the same job twice. It returns the job, the attempt, and the normalized workload spec, or `204 No Content` when nothing is assigned.

With `?wait=25s` (at most 30s) the request is held open until a job is assigned. The scheduler wakes waiting claims in the same process as soon as it commits; claims also re-check every 2 seconds, which covers assignments made by another replica. When the server starts shutting down, waiting claims return `204` immediately instead of delaying shutdown.

**Not yet handled.** Jobs assigned to a worker that goes offline stay `SCHEDULED`, and a claim whose response never reaches the worker leaves the job in `PREPARING`. Leases with expiry and reassignment, described in ADR 0003, arrive with recovery in a later milestone.

## Running a job

Each worker keeps one long-poll claim open whenever it has a free slot, and runs every claimed attempt in its own goroutine. An attempt runs as separate Docker containers that share one workspace volume ([ADR 0005](adr/0005-docker-runtime.md)):

```mermaid
sequenceDiagram
    participant W as Worker
    participant D as Docker
    participant A as API server
    W->>D: pull image, create volume tsuzuku-<attempt>
    W->>D: git container: fetch revision into /workspace
    W->>A: POST .../attempts/{aid}/executing (commit)
    Note over A: PREPARING to EXECUTING
    W->>D: step container: sh -c "<command>"
    W->>A: POST .../attempts/{aid}/verifying (exit code, timed out)
    Note over A: EXECUTING to VERIFYING, or FAILED on timeout
    W->>A: POST .../attempts/{aid}/finish
    Note over A: VERIFYING to COMPLETED (exit 0) or FAILED
    W->>D: remove volume
```

- **The worker reports facts; the server decides.** Reports carry the resolved commit, exit code, whether the step timed out, durations, and the container's image and limits. The server records each container in `runtimes` with an `attempt.runtime_finished` timeline event, then chooses the next state. A command that exits `0` completes the job; any other exit code, a timeout, or a failure outside the command (image pull, checkout, Docker error) fails it, with the reason in the history and on the attempt.
- **Reports are fenced.** `POST /api/v1/workers/{id}/attempts/{attempt_id}/{executing|verifying|finish}` checks, under a row lock, that the attempt belongs to that worker (`404` otherwise) and is still running (`409` otherwise). A duplicate or late report changes nothing.
- **Retries.** The worker retries a report on network errors and `5xx` responses for up to a minute, and stops on any `4xx`, since the server has already decided.
- **Shutdown.** On `SIGTERM` the worker kills its running containers and reports each attempt as failed ("worker stopped before the attempt finished") before exiting. Compose gives workers 30 seconds to do this.
- **Isolation.** Step containers drop all capabilities, run with a read-only root filesystem and a size-capped `/tmp`, have hard memory, CPU, and process limits, see no host paths, and have no network unless the workload asks for it. See ADR 0005 for the full list.

Verification (running `verification.command` and checking acceptance criteria), log streaming, artifacts, and cancellation of running jobs are added in the next slices; until then a job that reaches `VERIFYING` is decided by its exit code alone.

## Worker lifecycle

```mermaid
sequenceDiagram
    participant W as Worker
    participant A as API server
    participant DB as PostgreSQL
    W->>A: POST /workers/register (name, slots, capacity)
    A->>DB: upsert by name
    A-->>W: worker ID
    loop every heartbeat interval
        W->>A: POST /workers/{id}/heartbeat (cpu %, memory)
        A->>DB: update last_heartbeat_at, usage
    end
    Note over W,A: 404 on heartbeat: worker registers again
```

- **Registration is an upsert by name.** A restarted worker keeps its ID and registration time; capacity and metadata are refreshed. Registration retries with exponential backoff (1s to 30s) until the API is reachable.
- **Liveness is derived, not stored.** A worker is `online` when its last heartbeat is no older than `TSUZUKU_WORKER_STALE_AFTER` (15s default, three missed 5s heartbeats). The comparison uses the database clock for both sides, so clock skew between hosts cannot flip the status.
- **Host usage** (CPU percent, used memory) comes from the latest heartbeat. If sampling fails, the worker still sends a heartbeat with the previous sample, because liveness matters more than fresh numbers.
- **Docker is required.** A worker connects to the Docker daemon on startup and exits if it cannot, so a worker that could never run a job does not register. It then removes containers and volumes left behind by its own earlier runs (matched by the `dev.tsuzuku.worker` label).

## Dashboard

The dashboard (`frontend/`, Next.js) shows API liveness, database readiness, and the worker fleet, polling every 5 seconds.

The browser only talks to the dashboard's own origin. A route handler at `/api/[...path]` forwards an allowlisted set of `GET` paths (`/api/healthz`, `/api/readyz`, `/api/v1/*`) to the API at `TSUZUKU_API_URL`, read at request time. As a result:

- the Go API needs no CORS configuration;
- one dashboard image works in any environment, because the API address is not baked in at build time;
- an unreachable API turns into a `502` problem response instead of a browser network error.

## Local deployment

`compose.yaml` runs the whole system for development:

```mermaid
flowchart LR
    B[Browser] -->|:3000| FE[frontend]
    FE -->|/api proxy| S[server :8080]
    W1[worker-01] -->|HTTP + token| S
    W2[worker-02] -->|HTTP + token| S
    M[migrate] -->|goose up| DB[(postgres)]
    S --> DB
```

| Service                  | Image target            | Notes                                                                 |
| ------------------------ | ----------------------- | --------------------------------------------------------------------- |
| `postgres`               | `postgres:17-alpine`    | Published on `127.0.0.1:5433`                                         |
| `migrate`                | `Dockerfile` → `migrate` | One-shot `migrate up`; runs after Postgres is healthy                |
| `server`                 | `Dockerfile` → `server` | Starts only after `migrate` exits successfully; `127.0.0.1:8080`      |
| `worker-01`, `worker-02` | `Dockerfile` → `worker` | Fixed names, so restarts keep their registration                      |
| `frontend`               | `frontend/Dockerfile`   | Standalone Next.js server on `127.0.0.1:3000`                         |

The Go images are static binaries on a distroless, non-root base. All ports bind to localhost only. Compose falls back to a development worker token when `TSUZUKU_WORKER_TOKEN` is unset.

Workers mount the host's Docker socket (`/var/run/docker.sock`) and run as root to use it. Socket access is equivalent to root on the host, so a worker is a trusted, privileged process; the job containers it starts are not. Job containers and volumes are created on the host daemon next to the Compose services, named `tsuzuku-<attempt id>-<role>`.

## Data model

PostgreSQL holds workloads, jobs, their full transition history, attempts, leases, logs, verification results, failures, and deliveries. It is also the job queue: queued work is jobs in state `QUEUED`, claimed with `FOR UPDATE SKIP LOCKED` and owned through expiring leases ([ADR 0003](adr/0003-postgres-as-job-queue.md)). Access goes through `internal/store`: a `pgxpool` connection pool, a `WithTx` transaction helper, and sqlc-generated queries ([ADR 0002](adr/0002-postgres-access-pgx-sqlc-goose.md)). Only `server`, `migrate`, and `seed` read `TSUZUKU_DATABASE_URL`.

See [Database design](database-design.md) for the schema, conventions, and the invariants the database enforces.

## Continuous integration

Every push to `main` and every pull request runs `.github/workflows/ci.yml`:

| Job                | Checks                                                                                |
| ------------------ | ------------------------------------------------------------------------------------- |
| Go                 | `go mod tidy -diff`, `go vet`, golangci-lint, unit and integration tests with `-race` |
| Generated code     | `sqlc diff` fails if `internal/store/db` is stale                                     |
| Dashboard          | `pnpm` lint, typecheck, and production build                                          |
| Compose smoke test | Builds every image, starts the stack, waits for `/readyz` and two online workers, then runs a passing and a failing job end to end |

Third-party actions are pinned to commit SHAs. `task ci` runs the same checks locally, except the smoke test.

## Status

Milestone 0 (foundation) is complete: worker registration and heartbeats, the dashboard, the Compose stack, and CI. Milestone 1 has workload submission, the job state machine, the job read endpoints, scheduling and claiming, and running jobs in isolated Docker containers. Sections still to come: logs and artifacts, verification, cancellation, and evidence.

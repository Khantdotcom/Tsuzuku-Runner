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
| `GET /api/v1/workers`                 | none         | List workers with capacity, usage, and online status       |

Workers authenticate with a shared bearer token (`TSUZUKU_WORKER_TOKEN`), compared in constant time. Request and response types for the worker endpoints live in `internal/workerapi`, which both binaries import.

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

## Data model

PostgreSQL holds workloads, jobs, their full transition history, attempts, leases, logs, verification results, failures, and deliveries. Access goes through `internal/store`: a `pgxpool` connection pool, a `WithTx` transaction helper, and sqlc-generated queries ([ADR 0002](adr/0002-postgres-access-pgx-sqlc-goose.md)). Only `server`, `migrate`, and `seed` read `TSUZUKU_DATABASE_URL`.

See [Database design](database-design.md) for the schema, conventions, and the invariants the database enforces.

## Status

This document grows with each milestone. Sections still to come: job state machine, scheduling and claiming, runtime isolation, verification, and evidence.

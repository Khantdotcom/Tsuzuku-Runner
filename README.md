# Tsuzuku Runner 🏃‍♂️

[![CI](https://github.com/Khantdotcom/Tsuzuku-Runner/actions/workflows/ci.yml/badge.svg)](https://github.com/Khantdotcom/Tsuzuku-Runner/actions/workflows/ci.yml)

Tsuzuku (続く) in Japanese means "to continue" or "to keep on".

Tsuzuku Runner is an execution engine designed to manage, verify, and recover autonomous software workloads.

## Industry Problems

1. Agent Finished ≠ Software Is Correct: Agents are self-reporting thinking they did things right.

2. Dumb Retries Burn Money: Standard retry loops throw expensive tokens.

3. Resource Blindness: Agents aren't aware of real-time CPU, RAM, and infrastructure capacity.

## Core Focus

Survive runtime failures, retain execution context, and guarantee verifiable outputs.

## The Stack

Backend: Go (modular monolith)

State: PostgreSQL (transactional transitions, strict idempotency)

Compute: Docker / Linux VMs

Observability: OpenTelemetry, Prometheus, Grafana

## Development

### Run everything with Docker

The only requirement is [Docker](https://docs.docker.com/get-docker/) with Compose v2.

```bash
docker compose up --build
```

This starts PostgreSQL, applies migrations, then runs the API server, two workers (`worker-01`, `worker-02`), and the dashboard:

- Dashboard: <http://localhost:3000>
- API: <http://localhost:8080> (`/healthz`, `/readyz`, `/api/v1/workers`)

Stop a worker with `docker compose stop worker-02` and the dashboard shows it `offline` within about 15 seconds; `docker compose start worker-02` brings it back with the same ID. `docker compose down` stops the stack and keeps the database; add `-v` to wipe it.

### Prerequisites for host development

- [Go 1.27+](https://go.dev/dl/)
- [Task](https://taskfile.dev/) (`winget install Task.Task`, `brew install go-task`)
- [golangci-lint v2](https://golangci-lint.run/welcome/install/)
- [Docker](https://docs.docker.com/get-docker/) (PostgreSQL, sqlc, and integration tests)
- [Node.js 24](https://nodejs.org/) and pnpm (`corepack enable`) for the dashboard

### Host quickstart

```bash
cp .env.example .env   # database URL and a development worker token
task db:up             # PostgreSQL 17 on localhost:5433
task migrate:up        # create the schema
task seed              # optional: example job history
task run:server        # API on http://localhost:8080
task run:worker        # in a second terminal; registers and sends heartbeats
curl http://localhost:8080/readyz
curl http://localhost:8080/api/v1/workers
task fe:install && task fe:dev   # dashboard with hot reload on http://localhost:3000
```

The server needs the database to start. Stop the worker and it shows as `offline` after `TSUZUKU_WORKER_STALE_AFTER` (15s by default).

### Common tasks

| Command                 | What it does                                        |
| ----------------------- | --------------------------------------------------- |
| `task up` / `down` / `logs` | Start, stop, or follow the full Docker stack    |
| `task build`            | Build `bin/server`, `bin/worker`, and `bin/migrate` |
| `task test`             | Run unit tests                                      |
| `task test:integration` | Run unit and integration tests (needs Docker)       |
| `task lint`             | Run golangci-lint                                   |
| `task fmt`              | Format code (gofumpt + goimports)                   |
| `task db:up` / `db:down` / `db:reset` | Start, stop, or wipe the local database |
| `task migrate:up` / `migrate:down` / `migrate:status` | Manage schema migrations |
| `task migrate:create -- NAME` | Add a new SQL migration                       |
| `task sqlc`             | Regenerate query code after changing SQL            |
| `task fe:dev` / `fe:lint` / `fe:typecheck` / `fe:build` | Dashboard development and checks |
| `task ci`               | Run the CI checks locally (Go, sqlc drift, dashboard) |
| `task test:docker`      | Run unit and integration tests with `-race` in a Go container |
| `task lint:docker` / `fe:docker` | Run golangci-lint, or the dashboard lint, typecheck, and build, in a container |
| `task ci:docker`        | Run the full CI checks with only Docker on the host |
| `task smoke`            | Build and start an isolated stack, check workers and readiness, then remove it |

The `:docker` variants need only Docker and Task. Use them where host toolchains can't run, for example on Windows when Smart App Control blocks freshly built test binaries, `gcc` is missing for `-race`, or `pnpm` is blocked.

`task smoke` runs as a separate Compose project (`tsuzuku-smoke`) on ports 15433, 18080, and 13000, so it never touches the development stack or its data. Change the development ports with `TSUZUKU_PG_HOST_PORT`, `TSUZUKU_API_HOST_PORT`, and `TSUZUKU_UI_HOST_PORT`.

### Configuration

All configuration comes from environment variables prefixed with `TSUZUKU_`. See [`.env.example`](.env.example) for the full list and defaults.

## Documentation

- [Architecture](docs/architecture.md)
- [Database design](docs/database-design.md)
- [Architecture Decision Records](docs/adr/), including why [PostgreSQL is the job queue](docs/adr/0003-postgres-as-job-queue.md)

## License

[MIT](LICENSE)

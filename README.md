# Tsuzuku Runner 🏃‍♂️

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

### Configuration

All configuration comes from environment variables prefixed with `TSUZUKU_`. See [`.env.example`](.env.example) for the full list and defaults.

## Documentation

- [Architecture](docs/architecture.md)
- [Database design](docs/database-design.md)
- [Architecture Decision Records](docs/adr/)

## License

[MIT](LICENSE)

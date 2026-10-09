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

### Prerequisites

- [Go 1.27+](https://go.dev/dl/)
- [Task](https://taskfile.dev/) (`winget install Task.Task`, `brew install go-task`)
- [golangci-lint v2](https://golangci-lint.run/welcome/install/)
- [Docker](https://docs.docker.com/get-docker/) (PostgreSQL, sqlc, and integration tests)

### Quickstart

```bash
cp .env.example .env   # sets TSUZUKU_DATABASE_URL for the local database
task db:up             # PostgreSQL 17 on localhost:5433
task migrate:up        # create the schema
task seed              # optional: example job history
task run:server        # API on http://localhost:8080
task run:worker        # in a second terminal
curl http://localhost:8080/healthz
```

### Common tasks

| Command                 | What it does                                        |
| ----------------------- | --------------------------------------------------- |
| `task build`            | Build `bin/server`, `bin/worker`, and `bin/migrate` |
| `task test`             | Run unit tests                                      |
| `task test:integration` | Run unit and integration tests (needs Docker)       |
| `task lint`             | Run golangci-lint                                   |
| `task fmt`              | Format code (gofumpt + goimports)                   |
| `task db:up` / `db:down` / `db:reset` | Start, stop, or wipe the local database |
| `task migrate:up` / `migrate:down` / `migrate:status` | Manage schema migrations |
| `task migrate:create -- NAME` | Add a new SQL migration                       |
| `task sqlc`             | Regenerate query code after changing SQL            |

### Configuration

All configuration comes from environment variables prefixed with `TSUZUKU_`. See [`.env.example`](.env.example) for the full list and defaults.

## Documentation

- [Architecture](docs/architecture.md)
- [Database design](docs/database-design.md)
- [Architecture Decision Records](docs/adr/)

## License

[MIT](LICENSE)

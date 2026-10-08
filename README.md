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

### Quickstart

```bash
cp .env.example .env   # optional; every setting has a default
task run:server        # API on http://localhost:8080
task run:worker        # in a second terminal
curl http://localhost:8080/healthz
```

### Common tasks

| Command      | What it does                         |
| ------------ | ------------------------------------ |
| `task build` | Build `bin/server` and `bin/worker`  |
| `task test`  | Run unit tests                       |
| `task lint`  | Run golangci-lint                    |
| `task fmt`   | Format code (gofumpt + goimports)    |

### Configuration

All configuration comes from environment variables prefixed with `TSUZUKU_`. See [`.env.example`](.env.example) for the full list and defaults.

## Documentation

- [Architecture](docs/architecture.md)
- [Architecture Decision Records](docs/adr/)

## License

[MIT](LICENSE)

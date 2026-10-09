# Tsuzuku Runner — frontend

Next.js dashboard for Tsuzuku Runner. It shows API health and database readiness,
and polls the worker fleet every 5 seconds.

The browser only talks to this app. Requests to `/api/healthz`, `/api/readyz`, and
`/api/v1/*` are forwarded server-side to the Go API at `TSUZUKU_API_URL`
(default `http://localhost:8080`), so the API needs no CORS configuration.

## Development

Requires Node 24 and pnpm (via `corepack enable`). From the repository root:

```bash
task run:server        # API on :8080 (needs Postgres: task db:up && task migrate:up)
task fe:install
task fe:dev            # http://localhost:3000 with hot reload
```

Checks:

```bash
task fe:lint
task fe:typecheck
task fe:build
```

## Container

`docker compose up --build` from the repository root builds this app as a
standalone Next.js server and runs it on `127.0.0.1:3000`, pointed at the
`server` service.

## UI components

`src/components/ui` (Aceternity UI) and `src/components/animate-ui` (Animate UI)
are vendored from their registries (see `components.json`) and may be edited
locally.

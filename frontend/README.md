# Tsuzuku Runner — frontend

Next.js dashboard for Tsuzuku Runner. It shows API health and database readiness,
polls the worker fleet, and uses short-request polling for live logs and job
updates (Vercel Hobby–friendly; no long SSE).

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

Copy [`/.env.example`](../.env.example) or [`frontend/.env.example`](.env.example) and set
`TSUZUKU_API_URL` if the API is not on localhost:8080.

Checks:

```bash
task fe:lint
task fe:typecheck
task fe:build
```

## Deploy on Vercel (Hobby)

Only this dashboard belongs on Vercel. Postgres, the Go API, and Docker workers
must run elsewhere (local Compose, a VM, etc.) and expose a **public HTTPS** API.

1. Create a Vercel project with **Root Directory** `frontend`.
2. Set environment variable `TSUZUKU_API_URL` to your public API base
   (for example `https://api.example.com`, no trailing slash).
3. Deploy. The install/build commands are in [`vercel.json`](vercel.json).

> **Security:** the API has no user authentication yet. Anyone who can reach it
> can submit jobs (which run commands in containers with network access by
> default) and cancel them. Do not expose it to the internet until
> authentication lands; keep it on a private network or behind an
> authenticating proxy.

The API proxy uses short timeouts suitable for Hobby. Live logs poll
`GET /api/v1/jobs/{id}/logs` about once a second.

## Container

`docker compose up --build` from the repository root builds this app as a
standalone Next.js server (`DOCKER_BUILD=1`) and runs it on `127.0.0.1:3000`,
pointed at the `server` service.

## UI components

`src/components/ui` (Aceternity UI) and `src/components/animate-ui` (Animate UI)
are vendored from their registries (see `components.json`) and may be edited
locally.

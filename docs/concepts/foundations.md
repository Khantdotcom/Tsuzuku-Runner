# Foundations

The groundwork every backend service needs before it does anything interesting: starting up safely, reporting its health, stopping cleanly, and talking to its database and other processes reliably.

## Modular monolith

**The idea.** One codebase and a small number of binaries, split inside into packages with clear boundaries (`internal/job`, `internal/scheduler`, `internal/api`, ...). It is deployed like a monolith but organized like separate services.

**Why not microservices.** Microservices turn every function call between areas into a network call, which can be slow, can fail, and needs versioning. They pay off when many teams need to deploy independently. For one team, they mostly add moving parts. A modular monolith keeps the boundaries, so a package can be split out later if there is a real reason to.

**In this project.** `server` (API and scheduler) and `worker` are the two long-running binaries. Workers are separate because they must run on hosts with Docker; they talk to the server only over HTTP. See [ADR 0001](../adr/0001-modular-monolith.md).

## Fail-fast configuration

**The problem.** A typo in an environment variable that is only noticed hours later, when the code path that reads it finally runs.

**The idea.** Read *all* configuration into typed structs at startup and validate it straight away. If anything is wrong, refuse to start, with a message naming the variable.

**In this project.** `internal/config` reads `TSUZUKU_*` variables. Durations parse as durations, numbers as numbers, and rules such as "the scheduler interval is at least 100ms" are checked before the server opens a port. A process that starts is a process whose configuration is valid.

## Structured logging

**The idea.** Log lines are key-value records, not free text: `msg="job claimed" job_id=... attempt=1`. Machines can filter and aggregate them ("every log for job X"), and people can still read them.

**In this project.** `log/slog`, with readable text in development and JSON elsewhere. Every record carries `service=server` or `service=worker`. Log the IDs you would want to search by later.

## Liveness versus readiness

Two different questions that are easy to mix up:

- **Liveness** (`/healthz`): is the process running and able to answer at all? If not, restart it.
- **Readiness** (`/readyz`): can it do useful work right now, for example is the database reachable? If not, stop sending it traffic, but restarting it will not help.

Mixing them causes harm. If liveness checked the database, a short database outage would make the orchestrator restart every server at once, which turns a blip into an outage. Here, `/readyz` pings the database with a 2-second limit, and `/healthz` checks nothing but the process.

## Graceful shutdown

**The problem.** A deploy sends `SIGTERM`. If the process exits immediately, in-flight requests are cut off and half-finished work is lost.

**The idea.** On a stop signal: stop accepting new requests, let in-flight requests finish (up to a timeout), stop background loops, *then* close shared resources like the database pool. The order matters: closing the pool first would break the requests you are waiting for.

**In this project.** Both binaries handle `SIGINT` and `SIGTERM`. The server shuts down HTTP, waits for the scheduler loop to stop, and closes the pool last. Requests that could stay open for a long time need extra care; see [Long polling](long-polling.md#graceful-shutdown-with-open-requests).

## Schema migrations

**The idea.** The database schema is versioned like code. Each change is a numbered file (`migrations/00001_...sql`) with an `up` and a `down` step. A tool records which ones have run, so every environment (your laptop, CI, production) reaches the same schema the same way.

**Why a separate step.** `migrate` runs once, before the server starts (Compose enforces this order). If several server replicas each tried to migrate on startup, they would race each other.

**In this project.** goose, with migrations embedded in the binary. See [ADR 0002](../adr/0002-postgres-access-pgx-sqlc-goose.md).

## Type-safe SQL with code generation

**The trade-off.** An ORM hides SQL, which is convenient until you need precise control over locking and queries, as a job queue does. Hand-written SQL in strings gives you that control, but the compiler cannot check it: a renamed column fails only at runtime.

**The idea.** Write real SQL in `.sql` files and let a tool (sqlc) check it against the schema and generate Go functions with typed parameters and results. You keep full control of the SQL and get compile-time errors when it no longer matches the schema.

**In this project.** Queries live in `internal/store/queries/`, and generated code lives in `internal/store/db/`. CI fails if the generated code is stale.

## Connection pools

**The problem.** Opening a PostgreSQL connection is expensive (a TCP and TLS handshake, authentication, and a server process per connection). Opening one per request is slow, and opening too many overwhelms the database.

**The idea.** Keep a fixed set of open connections and lend them out. A request borrows one, uses it, and returns it.

**What to remember.** A transaction holds its connection for its whole lifetime. Keep transactions short, and never wait on something slow (like a 30-second long poll) while inside one. The claim endpoint waits *outside* any transaction for this reason.

**In this project.** `pgxpool`, opened in `store.Open`.

## IDs: UUIDv7

**Why UUIDs.** They can be generated anywhere, before the insert, without asking the database. That means you know a job's ID before writing it, and IDs from different places never collide.

**Why version 7.** Random UUIDs (v4) scatter new rows across the index, which hurts insert performance. UUIDv7 starts with a timestamp, so new IDs sort roughly by creation time and land at the end of the index, like an auto-increment number would.

**In this project.** Entities use UUIDv7. Jobs also get a small `number` (`#42`) because humans prefer that to a UUID.

## Upsert

**The idea.** "Insert this row, or update it if it already exists", in one atomic statement: `INSERT ... ON CONFLICT (name) DO UPDATE ...`.

**Why.** Checking first ("does it exist?") and then inserting is a race: two requests can both see "no" and both insert. An upsert lets the database settle it.

**In this project.** Worker registration is an upsert by name, so a restarted `worker-01` keeps its ID and history instead of appearing as a new worker.

## Heartbeats and derived liveness

**The idea.** Each worker sends "I'm alive" every 5 seconds. The server stores only the time of the last heartbeat. "Online" is *computed* when needed: the last heartbeat was within the last 15 seconds (three missed beats).

**Why derive it.** If you stored a status column, something would have to flip it to offline when heartbeats stop, and that something could itself crash. A timestamp cannot go stale; the comparison is always made fresh.

**Clock skew.** Different machines' clocks disagree, sometimes by seconds. If the worker sent its own timestamp and the server compared it with *its* clock, a fast or slow clock could flip the status. Here both sides of the comparison use the database clock (`now()` when writing and when reading), so only one clock is involved.

## Retries with exponential backoff

**The problem.** At startup a worker may come up before the server. If it retries in a tight loop, it wastes resources, and when many clients do this at once they can flood a recovering server.

**The idea.** Wait a little after the first failure, then double the wait each time, up to a cap: 1s, 2s, 4s, ... 30s. Production systems often add *jitter* (a random fraction of the wait) so many clients do not retry in lockstep.

**In this project.** `worker.Agent.register` backs off from 1s to 30s. If a heartbeat gets `404` (the server forgot this worker), the worker registers again.

## Constant-time comparison for secrets

**The problem.** A normal string comparison stops at the first differing byte. In principle, an attacker can measure how long a rejection takes and learn how many leading bytes of a guessed token were right, then guess the token byte by byte. This is a *timing attack*.

**The idea.** Compare secrets with a function whose running time does not depend on where they differ.

**In this project.** `requireWorkerToken` uses `crypto/subtle.ConstantTimeCompare`.

## Problem details for errors

**The idea.** Every error response has the same JSON shape (RFC 9457, `application/problem+json`): `status`, `title`, and a `detail` explaining what was wrong. Clients handle errors one way instead of special-casing each endpoint.

**Choosing status codes.**

- `400` means the request is malformed: broken JSON, an unknown field, a wrong type.
- `422` means it is well-formed but breaks a rule, like memory above the limit.
- `404` means the thing does not exist.
- `409` means it conflicts with the current state.
- `401` means the caller is not authenticated.

## A backend-for-frontend proxy

**The idea.** The browser talks only to the dashboard's own server, which forwards an allowlisted set of requests to the API.

**Why.** The browser never sees cross-origin requests, so the API needs no CORS setup. The API address is read at request time, so one dashboard image works everywhere. The allowlist keeps the proxy from becoming a doorway to everything.

**In this project.** `frontend/` has a route at `/api/[...path]` that forwards `GET` requests for `/api/healthz`, `/api/readyz`, and `/api/v1/*`.

## Testing against a real database

**The problem.** Mocking the database tests your assumptions about the database, not the database. Locking, constraints, and transactions, which are the things most likely to be wrong, are exactly what a mock cannot show.

**The idea.** Integration tests start a real, disposable PostgreSQL in Docker (Testcontainers). One container is shared per test package, and each test gets its own freshly migrated database, so tests are isolated and can run in parallel.

**In this project.** `internal/store/storetest`. Integration test files carry `//go:build integration`, so `go test ./...` stays fast, and `task test:integration` runs the full set.

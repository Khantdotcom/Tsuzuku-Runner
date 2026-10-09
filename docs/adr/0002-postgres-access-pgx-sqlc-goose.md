# ADR 0002: PostgreSQL access with pgx, sqlc, and goose

- **Status:** Accepted
- **Date:** 2026-10-09

## Context

PostgreSQL is the source of truth for every job ([ADR 0001](0001-modular-monolith.md)). The code that touches it has to make transactional state changes easy to get right, make the exact SQL visible for review, and keep schema changes versioned and repeatable on every machine.

Options considered:

- **An ORM (GORM, ent).** Fast to start, but generates SQL implicitly, which makes it harder to reason about locking, compare-and-set updates, and partial indexes, all of which this project depends on.
- **Hand-written queries with `pgx`.** Full control, but every query needs manual scanning code that drifts from the schema.
- **sqlc on top of `pgx`.** Queries are plain SQL files; sqlc type-checks them against the migrations and generates the Go code.

## Decision

- **Driver:** `pgx/v5` with `pgxpool`. Generated code targets pgx directly, not `database/sql`.
- **Queries:** sqlc generates `internal/store/db` from `internal/store/queries/*.sql`, using the migrations as the schema. The generated code is committed; `task sqlc:check` fails if it is stale. sqlc runs from a pinned Docker image so contributors do not need a C toolchain.
- **Migrations:** goose, used as a library with migrations embedded in the binary (`migrations.FS`). `cmd/migrate` applies them; numbering is sequential (`00001_...sql`) so ordering is unambiguous in review.
- **Transactions:** `store.WithTx` runs a function against a `*db.Queries` bound to a transaction, committing on success and rolling back on error.
- **Tests:** integration tests run against real PostgreSQL in Testcontainers, behind the `integration` build tag, so constraint and concurrency behavior is tested against the real engine rather than a mock.

## Consequences

- Every query is reviewable SQL, and schema/query mismatches fail at generation time instead of at runtime.
- Dynamic queries (for example, list filters with optional clauses) need either `sqlc.narg` patterns or a small amount of hand-written pgx code.
- goose needs a `database/sql` handle, so `cmd/migrate` opens one through the pgx stdlib adapter; application code never uses `database/sql`.
- Integration tests require Docker.

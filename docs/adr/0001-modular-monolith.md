# ADR 0001: Start as a modular monolith

- **Status:** Accepted
- **Date:** 2026-10-08

## Context

Tsuzuku Runner needs an API, a scheduler, workers, a runtime layer, verification, and evidence storage. These could be built as separate services from day one, but the project is early: domain boundaries are still moving, there is a single developer, and the main engineering goal is reliability of execution, not independent scaling of components.

Splitting into services now would add network hops, deployment complexity, and distributed failure modes before there is any measured need for them. It would also make cross-cutting changes (such as evolving the job state machine) touch several deployables at once.

## Decision

Build Tsuzuku Runner as a **modular monolith** in a single Go module:

- Two binaries: `server` (API + scheduler) and `worker` (execution). Workers are separate processes because they must run on hosts with a container runtime and must be able to crash independently, which is central to the recovery story.
- Domain logic lives in `internal/` packages with explicit boundaries. Packages communicate through Go interfaces, not shared global state.
- PostgreSQL is the single source of truth.

A component is extracted into its own service only when measured scale or isolation requirements justify it, and that extraction gets its own ADR.

## Consequences

- One repository, one build, one set of tooling; simple local development with Docker Compose.
- Refactoring across domain boundaries stays cheap while the design settles.
- The scheduler shares a process with the API, so it must be safe to run several server replicas (handled with a database lock when the scheduler is introduced).
- Package boundaries must be kept disciplined so a later extraction remains possible.

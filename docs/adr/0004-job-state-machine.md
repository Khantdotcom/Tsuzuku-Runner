# ADR 0004: Job state machine in Go, enforced by compare-and-set

- **Status:** Accepted
- **Date:** 2026-10-10

## Context

A job moves through `QUEUED`, `SCHEDULED`, `PREPARING`, `EXECUTING`, `VERIFYING`, and ends in `COMPLETED`, `FAILED`, or `CANCELLED`. Later milestones add `RETRYING`, `REPAIRING`, and `BLOCKED`. Several actors change job state: the API (submission, cancellation), the scheduler, and workers reporting progress. They run concurrently, and any of them can crash halfway through.

Every change has to be legal for the current state, applied at most once when two actors race, and recorded in the history (`state_transitions`) and the timeline (`job_events`) together with the change itself.

Options considered:

- **Transition rules in the database** (a trigger that rejects illegal changes). Impossible to bypass, but the rules live in PL/pgSQL, are hard to test, and every rule change is a migration.
- **A row lock** (`SELECT ... FOR UPDATE`, check the state in Go, then update). Correct, but holds a lock across a round trip and is easy to get wrong when a caller forgets the lock.
- **A transition table in Go plus a compare-and-set update.** The legal edges are a map in `internal/job`. The update is `UPDATE jobs SET state = $to WHERE id = $id AND state = $from`, so a concurrent change makes it match no row.

## Decision

Use the transition table in Go plus compare-and-set.

- `internal/job` owns the states and the table of legal edges. `job.Transition` is the only code that changes `jobs.state` after creation. It checks the table first, so an illegal change never reaches the database.
- The update, the `state_transitions` row, and a `job.state_changed` event are written in one transaction. If the compare-and-set matches no row, `Transition` reads the job once to tell the caller whether it does not exist (`ErrNotFound`) or is now in another state (`ErrConflict`). The caller decides whether to retry; nothing is overwritten.
- `started_at` is set the first time a job enters `PREPARING` and is never moved. `finished_at` is set when the job enters a terminal state.
- Only the edges a milestone actually uses are in the table. Milestone 1 allows the forward path, `FAILED` from `PREPARING`, `EXECUTING`, and `VERIFYING`, and `CANCELLED` from every non-terminal state. Retry, repair, and blocked edges are added with the features that use them.
- Submission writes the workload, the `QUEUED` job, its first transition (with no from-state), and a `job.created` event in one transaction. A client-supplied `Idempotency-Key` is stored on the workload under a unique constraint. Repeating a submission with the same key and the same normalized spec returns the original job; a different spec under the same key is rejected with `409 Conflict`. Two concurrent submissions with one key both resolve to the same job: the loser hits the unique constraint and returns the winner's job.

## Consequences

- The rules are plain Go and covered by an exhaustive table test over every pair of states.
- Concurrency safety does not depend on callers remembering to lock: the database rejects stale updates, and integration tests race two transitions to prove exactly one wins.
- Code that writes to `jobs.state` directly bypasses the table. Generated queries make this possible, so it is a review rule: only `internal/job` calls `TransitionJob`.
- Idempotency compares the normalized spec, so two requests that differ only in defaults (for example, an omitted image versus the default image spelled out) count as the same submission.

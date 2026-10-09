# ADR 0003: Use PostgreSQL as the job queue

- **Status:** Accepted
- **Date:** 2026-10-10

## Context

Jobs move from `QUEUED` to a worker and through their lifecycle. Something has to hold the queue, hand each job to exactly one worker, and notice when a worker disappears with a job in hand.

Job state already lives in PostgreSQL ([ADR 0001](0001-modular-monolith.md)), and every state change is a compare-and-set update written in the same transaction as its history row ([database design](../database-design.md)). Whatever holds the queue has to stay consistent with that state, including when a process crashes halfway through a change.

Options considered:

- **A dedicated broker (RabbitMQ, NATS JetStream, Redis streams, Kafka).** Good throughput and delivery features, but the queue then lives outside the database that owns job state. Every enqueue and every claim becomes a two-system write that needs an outbox or reconciliation to survive crashes, and there is one more stateful service to run, secure, and back up.
- **A job library built on PostgreSQL (River, Que, and similar).** Solves the same problem in the same database, but brings its own tables and job model. Tsuzuku's job is the product's core entity, with its own states, attempts, leases, and evidence, so the library's model would either duplicate or constrain it.
- **The `jobs` table itself as the queue.** Queued work is simply jobs in state `QUEUED`; claiming is a row lock plus a state transition.

## Decision

The `jobs` table is the queue. There is no separate broker.

- **Placement and claiming are separate steps.** The scheduler (one loop inside `server`, kept to a single active instance by a PostgreSQL advisory lock) picks a worker for each `QUEUED` job in FIFO order and moves it to `SCHEDULED`. The assigned worker then claims it over HTTP.
- **Claims use `SELECT ... FOR UPDATE SKIP LOCKED`** inside a transaction that also creates the `job_attempts` row, takes a lease, and transitions the job. Concurrent claimers skip rows another transaction holds instead of blocking on them, and a crash before commit leaves the job untouched.
- **Ownership is a lease, not a flag.** A lease has an `expires_at` that the worker extends with its heartbeat. A partial unique index allows at most one active lease per job. When a lease expires, the job is considered abandoned and the scheduler can reassign it.
- **Workers poll the API.** They never connect to the database ([architecture](../architecture.md)). Polling with a short long-poll is enough at MVP scale; `LISTEN/NOTIFY` can shorten pickup latency later without changing the model.

## Consequences

- Enqueue, claim, state change, and history are one transaction. There is no window in which the queue and the job state disagree, and no outbox to maintain.
- One fewer service to run. Backups, migrations, and observability for the queue are the database's.
- Throughput is bounded by PostgreSQL write capacity and lock traffic on `jobs`. That is far above what container-based jobs lasting seconds to minutes need, but it is the limit to watch.
- Queue queries need care: partial indexes on queued and scheduled jobs, short claim transactions, and bounded batch sizes, so a growing `jobs` table does not slow the hot path.
- Revisit this decision if sustained claim rates reach thousands per second, or if workloads need fan-out or delivery semantics that a single table cannot express cleanly. Moving to a broker at that point would get its own ADR.

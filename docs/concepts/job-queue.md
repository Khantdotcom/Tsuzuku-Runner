# Job queue

How a plain PostgreSQL table works as a job queue that several schedulers and workers can use at once without handing out the same job twice or overloading a worker.

## A table is a queue

**The idea.** There is no separate message broker. A job row in state `QUEUED` *is* the queued message. Taking it off the queue is a state change.

**What you get.** The queue is as durable as the database, sits in the same transactions as the rest of your data (a job and its history are created together), and can be queried directly ("what has been waiting longest?").

**What it costs.** You design the concurrency yourself, which is what the rest of this note is about. A dedicated broker scales further, but PostgreSQL handles thousands of jobs a second, far beyond what this system needs. See [ADR 0003](../adr/0003-postgres-as-job-queue.md).

## Placement versus claiming

Getting a job running is two separate steps:

1. **Placement (scheduler):** decide *where* the job runs. `QUEUED` becomes `SCHEDULED`, with `assigned_worker_id` set.
2. **Claiming (worker):** decide *when* it starts. The worker asks for its next assigned job; the job becomes `PREPARING` and an attempt row is created.

**Why split them.**

- The scheduler sees the whole fleet, so it can balance load. A worker grabbing jobs for itself would only know about itself.
- Workers *pull* work when they are ready, which is the **pull model**. In the **push model**, the server would call workers directly. That needs the server to reach every worker (hard through firewalls and NAT) and to handle a worker that is busy or gone in the middle of a push.

## Row locks: `FOR UPDATE`

`SELECT ... FOR UPDATE` locks the rows it returns until the transaction ends. Another transaction that tries to lock or update those rows waits.

That alone makes claiming safe but slow. Ten workers asking for the oldest job would line up behind the first one, and nine of them would wait only to find the job already taken.

## `SKIP LOCKED`

`SELECT ... FOR UPDATE SKIP LOCKED` changes the rule from "wait for locked rows" to "pretend they are not there".

```sql
SELECT * FROM jobs
WHERE state = 'SCHEDULED' AND assigned_worker_id = $1
ORDER BY created_at, number
LIMIT 1
FOR UPDATE SKIP LOCKED;
```

Two claimers running this at the same moment get *different* rows, or one gets a row and the other gets nothing. Neither waits. That is what turns a table into a queue that many consumers can use at once.

**In this project.** `LockQueuedJobs` (the scheduler) and `LockNextAssignedJob` (claiming). Indexes on `state` and `assigned_worker_id` keep these fast as the table grows.

## Write skew: when `SKIP LOCKED` is not enough

**The problem.** The scheduler's decision is not about one row. "Can worker-01 take another job?" depends on *how many* jobs it already has, which is a count across many rows.

Picture two scheduling rounds at the same moment. worker-01 has 2 slots and 1 job.

| Round A | Round B |
| ------- | ------- |
| Counts worker-01's jobs: 1 of 2 | Counts worker-01's jobs: 1 of 2 |
| Locks job #5 (`SKIP LOCKED`) | Locks job #6 (`SKIP LOCKED`, skips #5) |
| Places #5 on worker-01 | Places #6 on worker-01 |
| Commits | Commits |

worker-01 now has 3 jobs in 2 slots. Each round was correct on its own data and they never touched the same row, so no row lock could stop this. The mistake only shows up when you combine their results.

This is **write skew**: two transactions read the same facts, make decisions that are each valid, write *different* rows, and together break a rule.

**The usual fixes.**

1. Serialize the decision, so only one round runs at a time. This is what we do.
2. Lock the row that represents the shared resource (`SELECT ... FROM workers FOR UPDATE`), so rounds touching the same worker queue up.
3. Use the `SERIALIZABLE` isolation level and retry when the database reports a conflict.

## Advisory locks

**The idea.** A lock on a *number* rather than a row. PostgreSQL gives the number no meaning; the application decides what it stands for. Here the number means "the right to run a scheduling round".

```sql
SELECT pg_try_advisory_xact_lock(32778045701319541);
```

**The two choices in that call.**

- **`xact` (transaction-level)** rather than session-level: the lock is released automatically when the transaction commits or rolls back. A session-level lock lasts until the connection closes, and with a connection pool connections are reused, so a forgotten unlock could leave the lock held indefinitely.
- **`try_`** rather than waiting: if another server replica holds the lock, return `false` and skip this round. Waiting would only make the rounds queue up. Skipping costs at most one scheduler interval (1 second).

**In this project.** Every round in `scheduler.Tick` takes the lock first. The key is the ASCII bytes of "tsuzuku". This also makes running several server replicas safe: only one schedules at any moment, and if it crashes, its transaction ends and another replica takes over on the next tick.

## Mutation testing: proving a test matters

**The problem.** A concurrency test that passes might pass because the code is correct, or because the test never creates the race it claims to check.

**The idea.** Deliberately break the code (a *mutation*) and confirm that the test fails. If the test still passes without the guard, it was not testing the guard.

**In this project.** `TestConcurrentTicksNeverOverfill` runs 8 scheduling rounds at once against a worker with 2 slots and checks that exactly 2 jobs are placed. With the advisory lock removed, it overfills the worker and fails. With the lock restored, it passes. Now we know the test catches the bug it is meant to catch.

## Fair placement

The scheduler takes the oldest jobs first (first in, first out) and gives each one to the online worker with the fewest active jobs that still has a free slot. Ties go to the earliest-registered worker, which keeps results predictable and tests repeatable.

The placement logic (`scheduler.Place`) is a pure function: lists in, assignments out, with no database access. That makes the interesting decisions easy to unit-test, and keeps the database code to "load, decide, save".

## Leases (coming with recovery)

**The open problem.** If a worker crashes after claiming a job, the job sits in `PREPARING` forever. If a worker dies before claiming, its jobs sit in `SCHEDULED` forever.

**The idea.** A **lease** is ownership with an expiry time. The worker owns the job only until `expires_at` and must keep renewing it, much like a heartbeat for one job. If renewals stop, the lease expires, and a recovery process can safely give the job to someone else.

**The detail that makes it safe.** When a worker comes back after its lease expired, its late reports must be rejected, because the job may already belong to another attempt. Checking the attempt ID on every report (a *fencing* check) prevents two workers from both believing they own the job.

The `leases` table and its "at most one active lease per job" index already exist. Using them is planned for the recovery milestone.

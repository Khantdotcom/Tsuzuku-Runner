# Consistency

How the system keeps its data correct when requests fail halfway, arrive twice, or race each other.

## Transactions and atomicity

**The problem.** Submitting a workload writes four rows: the workload, the job, the first history entry, and a timeline event. If the process crashes after the second write, you have a job with no history: data that breaks the system's own rules.

**The idea.** Wrap related writes in a *transaction*. Either all of them become visible (commit) or none of them do (rollback). This is the "A" in ACID: **atomicity**.

The other letters, briefly:

- **Consistency:** constraints (`CHECK`, `UNIQUE`, foreign keys) hold after every commit.
- **Isolation:** concurrent transactions do not see each other's half-done work.
- **Durability:** once committed, the data survives a crash.

**In this project.** `store.WithTx(ctx, pool, func(q) error { ... })` begins a transaction, runs the function, and commits if it returns `nil` or rolls back otherwise. Submit, transition, schedule, and claim each run inside one.

**What to remember.** A transaction protects *database* writes only. If code inside one also calls an HTTP API or sends an email, a rollback cannot undo that. Keep side effects outside transactions, or record them as rows and act on them afterwards.

## State machines

**The problem.** A job has a state (`QUEUED`, `SCHEDULED`, `PREPARING`, ...). If any code can set any state, sooner or later something moves a `COMPLETED` job back to `EXECUTING`, and nobody can explain why.

**The idea.** List the legal transitions explicitly (`QUEUED` to `SCHEDULED` is allowed; `COMPLETED` to anything is not), and make one function the only way to change state. It checks the list before writing.

**In this project.** `internal/job/state.go` holds the transition table, and `job.Transition` is the only caller of the `TransitionJob` query. A unit test checks every pair of states against the table, so a change to the rules is a visible change to the test. See [ADR 0004](../adr/0004-job-state-machine.md).

## Compare-and-set (optimistic concurrency)

**The problem.** Two actors read a job as `SCHEDULED`. One cancels it, and the other starts it. Both write. Whichever writes second silently overwrites the first. This is the *lost update* problem.

**The idea.** Make the write conditional on what you believed the state was:

```sql
UPDATE jobs SET state = 'PREPARING'
WHERE id = $1 AND state = 'SCHEDULED';
```

If someone changed the job in the meantime, the `WHERE` matches nothing, zero rows are updated, and the caller knows it lost. Nothing is overwritten.

**Optimistic versus pessimistic.**

- **Optimistic** (compare-and-set): assume conflicts are rare, detect them at write time, and let the loser retry or give up. No locks are held while you think.
- **Pessimistic** (`SELECT ... FOR UPDATE`): lock the row first so nobody else can change it until you commit. That is safer when conflicts are common, but the lock is held across your logic, and it only protects you if every writer remembers to take it.

**In this project.** Every state change is a compare-and-set. A missed update becomes `ErrConflict` (the job is in another state now) or `ErrNotFound`. The queue also uses row locks, but only to *choose* rows (see [Job queue](job-queue.md)); the state change itself is still checked.

## Idempotency keys

**The problem.** A client submits a workload and the network drops before the response arrives. Did it work? If the client retries, it might create the job twice.

**The idea.** The client sends a unique `Idempotency-Key` header with the request. The server stores the key with what it created. A retry with the same key gets the *original* result instead of a new one. An operation is **idempotent** when doing it twice has the same effect as doing it once.

**The details that matter.**

- **Same key, same request:** return the original job (`200 OK`, `Idempotent-Replayed: true`).
- **Same key, different request:** the client has a bug. Reject it with `409 Conflict` instead of guessing.
- **Two identical requests at the same instant:** both check, both find nothing, and both try to insert. A `UNIQUE` constraint on the key makes the database pick a winner. The loser catches the constraint error and returns the winner's job. *The check is a shortcut; the constraint is the guarantee.*

**In this project.** `job.Service.Submit`, backed by `UNIQUE (workloads.idempotency_key)`.

## Validation and normalization

**Validation** rejects bad input with a clear reason: an `http://` repository URL, memory above the limit, a command longer than 4096 characters.

**Normalization** turns valid input into one canonical form: defaults filled in, the image tag made explicit, limits applied. The stored spec is always complete, and code downstream never has to ask "what if this field is missing?"

Normalization also makes idempotency fair. A request that leaves out the image and one that spells out the default image describe the same workload. After normalization they are equal, so the second one is treated as a retry rather than a conflict.

**In this project.** `workload.Request.Normalize` returns a `workload.Spec`. Strict JSON decoding rejects unknown fields, so a typo like `"comand"` is an error instead of being silently ignored.

## Append-only history

**The idea.** Do not overwrite the past. Alongside the job's *current* state, write a new row for every change (`state_transitions`), with who made it (`actor`) and why (`reason`). Attempts work the same way: a retry adds a row and does not reset the old one.

**Why.** "Why did this job fail?" can only be answered if the history still exists. It also makes debugging concurrency much easier: you can see the exact order of events.

**In this project.** `state_transitions` holds every move with a reason like `placed on worker-01 (0 of 2 slots busy)`. `job_events` is the timeline feed for the UI. Foreign keys use `NO ACTION`, so history is never deleted as a side effect.

## Cursor (keyset) pagination

**The problem with `OFFSET`.** `LIMIT 20 OFFSET 40` makes the database read and discard 40 rows, which gets slower as the offset grows. Worse, if new rows arrive between page requests, everything shifts and you see duplicates or skip rows.

**The idea.** Page by position instead: "give me 20 jobs with a number lower than 1234". The last item on a page becomes the cursor for the next one. An index makes each page equally fast, and new rows do not shift earlier pages.

**In this project.** `GET /jobs?before=<number>`, and `?after=<id>` for events and logs. The `bigint` keys double as cursors.

## Constraints as the last line of defence

Application code checks rules, but only the database can check them *across every concurrent request*. So the important invariants are also constraints:

- `CHECK` on state columns (text plus `CHECK`, not PostgreSQL enums, because adding a value is then a one-line migration);
- `UNIQUE` on idempotency keys and on `(job_id, attempt_number)`;
- a partial unique index allowing at most one active lease per job.

If two code paths race past the application checks, the constraint still refuses the second write. The full list is in [Database design](../database-design.md#invariants-enforced-by-the-database).

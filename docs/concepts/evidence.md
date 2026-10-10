# Logs, evidence, and cancellation

How the system captures what a job printed, decides and explains its outcome, and stops a running job on request, while networks drop messages and disks fill up.

## Streaming output in batches

**The problem.** A command prints output over minutes. Sending every line as its own request floods the server; sending everything at the end shows nothing while the job runs and loses it all if the worker crashes.

**The idea.** **Batch**: collect output and send it when either a time limit or a size limit is reached, whichever comes first. The time limit bounds how stale the view is; the size limit bounds memory and request size.

**In this project.** `worker.logShipper` cuts output into chunks of at most 16 KB and uploads them every second, or as soon as 64 KB are waiting.

## Exactly once from "at least once"

**The problem.** The worker sends a batch, the server stores it, and the response is lost. The worker cannot tell whether the batch arrived, so it must resend. Now the output is stored twice.

**The idea.** Networks only offer **at-least-once** delivery: retry until acknowledged, so duplicates happen. To get the effect of **exactly once**, give every message a stable identity and make the receiver ignore an identity it already has. This is the same idea as [idempotency keys](consistency.md#idempotency-keys), at the level of single messages.

**In this project.** Each chunk has a sequence number (`seq`) that is fixed when the output is captured, not when it is sent. The server inserts with `ON CONFLICT (attempt_id, seq) DO NOTHING`, backed by a `UNIQUE` constraint. A resent batch changes nothing. The numbers also give the chunks their order across stdout and stderr.

## Bounded buffers

**The problem.** A command prints gigabytes, or the server is down for an hour while output keeps coming. An unbounded buffer grows until the process runs out of memory, and then *everything* is lost, including the jobs that were behaving.

**The idea.** Every buffer gets a limit, and you decide in advance what happens at the limit: block the producer (**backpressure**), or drop data and say so. Blocking a running program is not an option here, so the system drops, and records that it did.

**In this project.**

- The server stores at most `TSUZUKU_MAX_LOG_BYTES` per attempt (10 MiB). The batch that crosses the limit is cut, an `attempt.logs_truncated` event is recorded once, and the response tells the worker to stop sending.
- The worker holds at most 4 MiB of unsent output per attempt while the server is unreachable, and logs how much it dropped.
- Verification output is reduced to the last 4 KB (`runtime.TailBuffer`): for a failing check, the end of the output is usually where the error is.

## Atomic file writes

**The problem.** The server is writing `stdout.log` when it crashes. A reader later finds half a file and cannot tell it is incomplete.

**The idea.** Write to a temporary file in the same directory, then **rename** it to the final name. A rename within one filesystem is atomic: readers see either no file or the whole file, never a partial one.

**In this project.** `artifact.FS.Put` writes `<key>.tmp-<random>` and renames it into place.

## Path traversal

**The problem.** A file is stored under a key built from IDs. If any part of a key can contain `../`, it can point outside the storage directory, and a write could overwrite the server's own files.

**The idea.** Validate keys, *and* resolve every path inside a fixed root in a way that cannot escape, even through symbolic links. Checking strings alone tends to miss a case; making escape impossible does not.

**In this project.** `artifact.FS` uses Go's `os.Root`, which refuses any path that leaves the directory, and `validKey` also rejects backslashes, colons, and anything `fs.ValidPath` rejects.

## Checksums

Every artifact is recorded with its size and **SHA-256** hash. The hash proves a downloaded file is exactly what was stored, detects silent corruption on disk, and doubles as the HTTP `ETag`, so a client can ask "has this changed?" without downloading it again.

## Side effects after commit

**The problem.** Writing a file cannot be rolled back with a transaction (see [Transactions](consistency.md#transactions-and-atomicity)). If the file is written inside the transaction and the transaction then fails, the file is orphaned. If a failing file write fails the transaction, a full disk would turn a passing job into an error.

**The idea.** Commit the decision first, then do the side effect as **best effort**: if it fails, log it, and make sure the system is still correct without it. Give the side effect its own deadline, detached from the request that triggered it, so a client disconnecting does not abort it halfway.

**In this project.** After an attempt's outcome commits, the server builds `stdout.log` and `stderr.log` from the stored chunks, using `context.WithoutCancel` and a 30-second timeout. If that fails, the outcome stands and the chunks are still readable from the logs endpoint. Both the file write and the metadata insert are idempotent, so doing it again is safe.

## Verification independent of the executor

**The problem.** "The command exited 0" only says the command thinks it succeeded. An AI agent's script can exit 0 after doing nothing useful.

**The idea.** Check the result with a separate step whose only job is to judge: run tests, look for a file, compare output. It runs in a fresh container, so the executor cannot tamper with it. Each rule becomes a **check** with its own status, so a result says *which* rule failed.

**Combining statuses.** When several checks disagree, the more serious status wins: `ERROR` (the check itself could not run) beats `FAILED` (it ran and said no), which beats `PASSED`. A check that cannot apply is `SKIPPED` rather than failed. There is no point verifying a command that already failed.

**In this project.** `job.judge` in `internal/job/outcome.go` turns the reported facts into checks and an outcome. It is a pure function with no database or network, so a table-driven unit test covers every combination.

## Failure classification

**The problem.** "Job failed" is not actionable. Was it the user's code, the network, a missing image, or our own bug? Each needs a different response: fix the code, retry, or page someone.

**The idea.** Record every failure with a **category** from a fixed list, plus a message and structured details. Categories make failures searchable and countable, and later drive automatic decisions such as retrying infrastructure failures but not test failures.

**In this project.** The worker tags early failures with the *stage* that failed (`image`, `checkout`, `execute`, ...). `job.stageCategory` maps stages to categories, so the mapping lives in one tested place on the server.

## Cooperative cancellation

**The problem.** You cannot safely kill a goroutine from the outside in Go, and killing a process mid-write can corrupt what it was writing. Stopping work has to be arranged with the work itself.

**The idea.** **Cooperative cancellation**: the code doing the work regularly checks a "please stop" signal and cleans up when it sees it. In Go that signal is a `context.Context`: cancelling it closes `ctx.Done()`, and every blocking call that takes the context returns.

**Why it stopped matters.** A worker stops an attempt for two different reasons: the server asked (report *cancelled*), or the worker itself is shutting down (report *failed*: the job did not get a fair run). `context.WithCancelCause` attaches a reason to the cancellation, and `context.Cause(ctx)` reads it back where the report is built.

**In this project.** `Executor.Cancel` cancels one attempt with `errCancelRequested`; other attempts on the worker keep running.

## Piggybacking on an existing channel

**The problem.** The server wants to tell a worker "stop attempt X". But workers only make outgoing requests; they open no port, and may sit behind a firewall or NAT, so the server cannot call them.

**The idea.** Put the message in the response to a request the worker already makes. Workers send a heartbeat every 5 seconds, so its response carries the list of attempts to cancel. No new connection, port, or endpoint is needed. The cost is latency: up to one heartbeat interval.

**In this project.** `HeartbeatResponse.CancelAttempts`. The server keeps listing an attempt until the worker reports it finished, so a lost heartbeat response only delays the cancel.

## Deciding races with a rule

**The problem.** A user cancels a job just as its command finishes successfully. The worker reports "exit 0" and the cancel request arrives at about the same time. Which one wins depends on timing, so the result is unpredictable.

**The idea.** Do not let timing pick. Choose a rule and apply it in one place under a lock: *a pending cancellation always wins*. Every interleaving then gives the same answer.

**In this project.** The `verifying` and `finish` reports read `jobs.cancel_requested_at` while holding the attempt's row lock. If it is set, the attempt ends `CANCELLED` whatever the worker reported. `TestCancelRequestWinsOverWorkerError` covers the case where the worker reports an error instead.

## Making text safe to store

Process output is bytes, not text. It may not be valid UTF-8, and it may contain `NUL` bytes, which PostgreSQL `text` columns reject outright. Raw output is stored as `bytea` in `log_chunks`. The verification output kept on a check is cleaned first (invalid UTF-8 replaced, `NUL` removed, trimmed from the front) so that one odd byte cannot make the whole report fail to save.

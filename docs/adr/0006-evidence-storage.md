# ADR 0006: Stream logs to the server and store evidence files on its filesystem

- **Status:** Accepted
- **Date:** 2026-10-11

## Context

Every outcome must be explainable after the fact: what the command printed, what verification found, and why the job failed. Output has to be visible while a job runs, survive a worker crash or a lost response, and be downloadable as whole files afterwards. Workers hold no database credentials and talk to the server only over HTTP ([ADR 0001](0001-modular-monolith.md)).

Options considered for output:

- **Workers upload whole log files when a step ends.** Simple, but nothing is visible while the job runs, and a worker that crashes mid-step loses everything.
- **Workers stream numbered chunks to the server, which stores them in `log_chunks`.** Output is visible at once and stored exactly once even when a batch is resent. The server can join the chunks into files when the attempt ends.

Options considered for file storage:

- **S3-compatible object storage (MinIO in development).** The usual production choice, but one more service to run and configure before there is any file larger than a log.
- **The server's local filesystem behind a small interface.** No extra service; enough for one server replica.
- **Files in PostgreSQL.** Already there, but large blobs bloat the database and its backups.

Options considered for cancelling a running job:

- **The server calls the worker.** Workers would need to accept inbound connections and the server would need their addresses.
- **A dedicated long-poll for cancel requests.** Prompt, but a second open connection per worker.
- **The heartbeat response lists attempts to cancel.** Workers already call every few seconds; no new connection or endpoint.

## Decision

- **Logs.** The worker numbers chunks (`seq`, at most 16 KB each) across stdout and stderr and uploads them in batches of up to 64 KB every second. The server stores them with `ON CONFLICT (attempt_id, seq) DO NOTHING`, under the same attempt lock and ownership check as every other report.
- **Log limit.** The server stores at most `TSUZUKU_MAX_LOG_BYTES` per attempt, records `attempt.logs_truncated` once when the limit is crossed, and tells the worker to stop sending. The worker keeps at most 4 MiB unsent while the server is unreachable and drops the rest.
- **Artifacts.** When an attempt ends, the server joins its chunks into `stdout.log` and `stderr.log` and stores them through `artifact.Store`, recording each in `artifacts` with size and SHA-256. The only implementation is `artifact.FS`, rooted at `TSUZUKU_ARTIFACT_DIR`. Keys look like `jobs/<job id>/attempts/<n>/stdout.log`; writes go to a temporary file that is renamed into place, and every path is resolved inside the root with `os.Root`, so a key cannot reach outside it.
- **After commit, best effort.** Files are written after the attempt's outcome commits, with a context that ignores the request's cancellation and a 30-second limit. A failure is logged and changes nothing: the outcome stands, and the chunks remain readable from the logs endpoint.
- **Verification and failures** are rows (`verification_runs`, `verification_checks`, `failures`) written in the same transaction as the outcome they explain. Verification output is a 4 KB tail on the check, cleaned to valid UTF-8 without NUL bytes.
- **Cancellation** of a running job sets `jobs.cancel_requested_at`. Each heartbeat response lists the worker's attempts whose job has a pending request; the worker stops those attempts and reports them as cancelled. A pending request decides the outcome even if the worker reports something else.

## Consequences

- Artifacts live on one server's disk. Several server replicas, or a server without a persistent volume, need an object-storage implementation of `artifact.Store`; nothing else changes.
- The same output is stored twice, as chunks and as files. Chunks serve live viewing and paging; files serve download and long-term evidence. Old chunks can be pruned later without losing evidence.
- A crash between the outcome commit and the file write leaves an attempt without its log files. Writing them again from the chunks is safe, because the write and the metadata insert are both idempotent, but nothing retries it yet.
- A running job stops within one heartbeat interval of the request (5 seconds by default), not instantly.
- Workers never reach storage directly, so they need no storage credentials, and the server checks every byte against the attempt's owner and limit.

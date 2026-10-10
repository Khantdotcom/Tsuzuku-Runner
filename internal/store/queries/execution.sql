-- name: CreateAttempt :one
INSERT INTO job_attempts (id, job_id, attempt_number, worker_id)
VALUES (@id, @job_id, @attempt_number, @worker_id)
RETURNING *;

-- name: NextAttemptNumber :one
SELECT (coalesce(max(attempt_number), 0) + 1)::integer AS next
FROM job_attempts
WHERE job_id = @job_id;

-- name: ListAttempts :many
SELECT * FROM job_attempts
WHERE job_id = @job_id
ORDER BY attempt_number;

-- name: LockAttempt :one
-- LockAttempt serializes reports about one attempt for the rest of the transaction.
SELECT * FROM job_attempts
WHERE id = @id
FOR UPDATE;

-- name: RecordAttemptExit :exec
UPDATE job_attempts
SET exit_code = @exit_code
WHERE id = @id;

-- name: FinishAttempt :one
-- FinishAttempt ends a RUNNING attempt; it returns no row if the attempt has
-- already finished.
UPDATE job_attempts
SET status      = @status,
    error       = sqlc.narg(error),
    finished_at = now()
WHERE id = @id AND status = 'RUNNING'
RETURNING *;

-- name: CreateRuntime :one
INSERT INTO runtimes (
    id, attempt_id, kind, role, image, container_id, volume_name,
    cpu_millis, memory_mb, network_enabled, created_at, destroyed_at
) VALUES (
    @id, @attempt_id, 'docker', @role, @image, @container_id, @volume_name,
    @cpu_millis, @memory_mb, @network_enabled, @created_at, @destroyed_at
)
RETURNING *;

-- name: ListRuntimes :many
SELECT r.* FROM runtimes r
JOIN job_attempts a ON a.id = r.attempt_id
WHERE a.job_id = @job_id
ORDER BY r.created_at, r.id;

-- name: AcquireLease :one
INSERT INTO leases (id, job_id, attempt_id, worker_id, expires_at)
VALUES (@id, @job_id, @attempt_id, @worker_id, @expires_at)
RETURNING *;

-- name: AppendLogChunk :one
INSERT INTO log_chunks (job_id, attempt_id, seq, stream, data)
VALUES (@job_id, @attempt_id, @seq, @stream, @data)
RETURNING *;

-- name: ListLogChunks :many
SELECT * FROM log_chunks
WHERE job_id = @job_id AND id > @after_id
ORDER BY id
LIMIT @row_limit;

-- name: InsertLogChunk :execrows
-- InsertLogChunk stores one chunk; a chunk re-sent with the same seq is ignored.
INSERT INTO log_chunks (job_id, attempt_id, seq, stream, data)
VALUES (@job_id, @attempt_id, @seq, @stream, @data)
ON CONFLICT (attempt_id, seq) DO NOTHING;

-- name: AttemptLogBytes :one
SELECT coalesce(sum(octet_length(data)), 0)::bigint AS total
FROM log_chunks
WHERE attempt_id = @attempt_id;

-- name: ListAttemptLogChunks :many
SELECT * FROM log_chunks
WHERE attempt_id = @attempt_id
ORDER BY seq;

-- name: ListCancelRequestedAttempts :many
-- ListCancelRequestedAttempts returns a worker's running attempts whose job
-- has a pending cancellation request.
SELECT a.id FROM job_attempts a
JOIN jobs j ON j.id = a.job_id
WHERE a.worker_id = @worker_id
  AND a.status = 'RUNNING'
  AND j.cancel_requested_at IS NOT NULL
ORDER BY a.started_at;

-- name: CreateArtifact :execrows
-- CreateArtifact ignores a second row for the same job, attempt, and name.
INSERT INTO artifacts (id, job_id, attempt_id, name, content_type, storage_key, size_bytes, sha256)
VALUES (@id, @job_id, @attempt_id, @name, @content_type, @storage_key, @size_bytes, @sha256)
ON CONFLICT DO NOTHING;

-- name: ListArtifacts :many
SELECT * FROM artifacts
WHERE job_id = @job_id
ORDER BY created_at, name;

-- name: GetArtifact :one
SELECT * FROM artifacts
WHERE id = @id AND job_id = @job_id;

-- name: CreateVerificationRun :one
INSERT INTO verification_runs (id, job_id, attempt_id, status, started_at, finished_at)
VALUES (@id, @job_id, @attempt_id, @status, @started_at, @finished_at)
RETURNING *;

-- name: CreateVerificationCheck :exec
INSERT INTO verification_checks (
    id, verification_run_id, name, kind, command, status, exit_code, output, started_at, finished_at
) VALUES (
    @id, @verification_run_id, @name, @kind, sqlc.narg(command), @status, sqlc.narg(exit_code),
    @output, sqlc.narg(started_at), sqlc.narg(finished_at)
);

-- name: ListVerificationRuns :many
SELECT * FROM verification_runs
WHERE job_id = @job_id
ORDER BY started_at, id;

-- name: ListVerificationChecks :many
SELECT c.* FROM verification_checks c
JOIN verification_runs r ON r.id = c.verification_run_id
WHERE r.job_id = @job_id
ORDER BY c.verification_run_id, c.id;

-- name: CreateFailure :exec
INSERT INTO failures (id, job_id, attempt_id, category, message, details)
VALUES (@id, @job_id, @attempt_id, @category, @message, @details);

-- name: ListFailures :many
SELECT * FROM failures
WHERE job_id = @job_id
ORDER BY created_at, id;

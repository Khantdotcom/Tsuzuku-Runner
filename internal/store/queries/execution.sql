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

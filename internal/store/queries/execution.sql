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

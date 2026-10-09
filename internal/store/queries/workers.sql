-- name: CreateWorker :one
INSERT INTO workers (id, name, slots, cpu_millis, memory_mb, metadata)
VALUES (@id, @name, @slots, @cpu_millis, @memory_mb, @metadata)
RETURNING *;

-- name: GetWorkerByName :one
SELECT * FROM workers
WHERE name = @name;

-- name: UpsertWorker :one
-- UpsertWorker registers a worker by name. A worker that registers again
-- (for example after a restart) keeps its ID and registered_at.
INSERT INTO workers (id, name, slots, cpu_millis, memory_mb, metadata)
VALUES (@id, @name, @slots, @cpu_millis, @memory_mb, @metadata)
ON CONFLICT (name) DO UPDATE SET
    slots             = EXCLUDED.slots,
    cpu_millis        = EXCLUDED.cpu_millis,
    memory_mb         = EXCLUDED.memory_mb,
    metadata          = EXCLUDED.metadata,
    last_heartbeat_at = now()
RETURNING *;

-- name: RecordHeartbeat :execrows
UPDATE workers
SET last_heartbeat_at = now(),
    cpu_used_percent  = @cpu_used_percent,
    memory_used_mb    = @memory_used_mb
WHERE id = @id;

-- name: ListWorkers :many
-- ListWorkers also returns the database clock so liveness is judged against
-- the same clock that stamped last_heartbeat_at.
SELECT sqlc.embed(workers), now()::timestamptz AS db_now
FROM workers
ORDER BY name;

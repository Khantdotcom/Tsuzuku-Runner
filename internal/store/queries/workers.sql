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

-- name: WorkerExists :one
SELECT EXISTS (SELECT 1 FROM workers WHERE id = @id) AS found;

-- name: ListSchedulableWorkers :many
-- ListSchedulableWorkers returns online workers, oldest registration first,
-- with the number of jobs they hold that have not finished. Liveness uses the
-- database clock, the same clock that stamped last_heartbeat_at.
SELECT w.id, w.name, w.slots, count(j.id)::integer AS active_jobs
FROM workers w
LEFT JOIN jobs j
    ON j.assigned_worker_id = w.id
   AND j.state IN ('SCHEDULED', 'PREPARING', 'EXECUTING', 'VERIFYING')
WHERE w.last_heartbeat_at >= now() - make_interval(secs => @stale_after_seconds::double precision)
GROUP BY w.id
ORDER BY w.registered_at, w.id;

-- name: ListWorkers :many
-- ListWorkers also returns the database clock so liveness is judged against
-- the same clock that stamped last_heartbeat_at.
SELECT sqlc.embed(workers), now()::timestamptz AS db_now
FROM workers
ORDER BY name;

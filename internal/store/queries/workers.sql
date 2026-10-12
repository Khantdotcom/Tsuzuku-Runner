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
-- with unfinished job counts and reserved CPU/RAM from assigned workloads.
-- Liveness uses the database clock, the same clock that stamped last_heartbeat_at.
SELECT w.id, w.name, w.slots, w.cpu_millis, w.memory_mb,
       count(j.id)::integer AS active_jobs,
       coalesce(sum(wl.cpu_millis), 0)::integer AS reserved_cpu_millis,
       coalesce(sum(wl.memory_mb), 0)::integer AS reserved_memory_mb
FROM workers w
LEFT JOIN jobs j
    ON j.assigned_worker_id = w.id
   AND j.state IN ('SCHEDULED', 'PREPARING', 'EXECUTING', 'VERIFYING')
LEFT JOIN workloads wl ON wl.id = j.workload_id
WHERE w.last_heartbeat_at >= now() - make_interval(secs => @stale_after_seconds::double precision)
GROUP BY w.id
ORDER BY w.registered_at, w.id;

-- name: ListWorkers :many
-- ListWorkers also returns the database clock so liveness is judged against
-- the same clock that stamped last_heartbeat_at, plus reserved capacity from
-- unfinished assigned jobs.
SELECT sqlc.embed(workers), now()::timestamptz AS db_now,
       coalesce(active.active_jobs, 0)::integer AS active_jobs,
       coalesce(active.reserved_cpu_millis, 0)::integer AS reserved_cpu_millis,
       coalesce(active.reserved_memory_mb, 0)::integer AS reserved_memory_mb
FROM workers
LEFT JOIN LATERAL (
    SELECT count(j.id)::integer AS active_jobs,
           coalesce(sum(wl.cpu_millis), 0)::integer AS reserved_cpu_millis,
           coalesce(sum(wl.memory_mb), 0)::integer AS reserved_memory_mb
    FROM jobs j
    JOIN workloads wl ON wl.id = j.workload_id
    WHERE j.assigned_worker_id = workers.id
      AND j.state IN ('SCHEDULED', 'PREPARING', 'EXECUTING', 'VERIFYING')
) active ON true
ORDER BY workers.name;

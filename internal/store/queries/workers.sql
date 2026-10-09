-- name: CreateWorker :one
INSERT INTO workers (id, name, slots, cpu_millis, memory_mb, metadata)
VALUES (@id, @name, @slots, @cpu_millis, @memory_mb, @metadata)
RETURNING *;

-- name: GetWorkerByName :one
SELECT * FROM workers
WHERE name = @name;

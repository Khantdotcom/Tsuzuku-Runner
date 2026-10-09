-- name: CreateWorkload :one
INSERT INTO workloads (
    id, idempotency_key, repository_url, revision, command, image, verification_command,
    acceptance_criteria, cpu_millis, memory_mb, timeout_seconds, network_enabled, spec
) VALUES (
    @id, @idempotency_key, @repository_url, @revision, @command, @image, @verification_command,
    @acceptance_criteria, @cpu_millis, @memory_mb, @timeout_seconds, @network_enabled, @spec
)
RETURNING *;

-- name: CreateJob :one
INSERT INTO jobs (id, workload_id, scheduled_at)
VALUES (@id, @workload_id, @scheduled_at)
RETURNING *;

-- name: GetJob :one
SELECT * FROM jobs
WHERE id = @id;

-- name: TransitionJob :one
-- TransitionJob is a compare-and-set: it returns no row when the job is no
-- longer in from_state, so concurrent transitions cannot both succeed.
UPDATE jobs
SET state = @to_state, updated_at = now()
WHERE id = @id AND state = @from_state
RETURNING *;

-- name: RecordTransition :one
INSERT INTO state_transitions (job_id, from_state, to_state, actor, reason)
VALUES (@job_id, @from_state, @to_state, @actor, @reason)
RETURNING *;

-- name: ListTransitions :many
SELECT * FROM state_transitions
WHERE job_id = @job_id
ORDER BY id;

-- name: CountJobsByState :many
SELECT state, count(*) AS count
FROM jobs
GROUP BY state
ORDER BY state;

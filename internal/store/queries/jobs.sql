-- name: CreateWorkload :one
INSERT INTO workloads (
    id, idempotency_key, repository_url, revision, command, image, verification_command,
    acceptance_criteria, cpu_millis, memory_mb, timeout_seconds, network_enabled, spec
) VALUES (
    @id, @idempotency_key, @repository_url, @revision, @command, @image, @verification_command,
    @acceptance_criteria, @cpu_millis, @memory_mb, @timeout_seconds, @network_enabled, @spec
)
RETURNING *;

-- name: GetWorkload :one
SELECT * FROM workloads
WHERE id = @id;

-- name: GetWorkloadByIdempotencyKey :one
SELECT * FROM workloads
WHERE idempotency_key = @idempotency_key;

-- name: CreateJob :one
INSERT INTO jobs (id, workload_id, scheduled_at)
VALUES (@id, @workload_id, @scheduled_at)
RETURNING *;

-- name: GetJob :one
SELECT * FROM jobs
WHERE id = @id;

-- name: GetJobByNumber :one
SELECT * FROM jobs
WHERE number = @number;

-- name: GetFirstJobForWorkload :one
SELECT * FROM jobs
WHERE workload_id = @workload_id
ORDER BY number
LIMIT 1;

-- name: ListJobs :many
-- ListJobs returns jobs newest first. A NULL state matches every state, and
-- before is a job number cursor: only jobs numbered below it are returned.
SELECT sqlc.embed(jobs), w.repository_url, w.revision, w.command, w.image
FROM jobs
JOIN workloads w ON w.id = jobs.workload_id
WHERE (sqlc.narg(state)::text IS NULL OR jobs.state = sqlc.narg(state)::text)
  AND (sqlc.narg(before)::bigint IS NULL OR jobs.number < sqlc.narg(before)::bigint)
ORDER BY jobs.number DESC
LIMIT @row_limit;

-- name: TransitionJob :one
-- TransitionJob is a compare-and-set: it returns no row when the job is no
-- longer in from_state, so concurrent transitions cannot both succeed.
-- started_at keeps the first start; finished_at is set on every final state.
UPDATE jobs
SET state       = @to_state,
    updated_at  = now(),
    started_at  = CASE WHEN @mark_started::boolean THEN coalesce(started_at, now()) ELSE started_at END,
    finished_at = CASE WHEN @mark_finished::boolean THEN now() ELSE finished_at END
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

-- name: CreateJobEvent :one
INSERT INTO job_events (job_id, attempt_id, type, payload)
VALUES (@job_id, @attempt_id, @type, @payload)
RETURNING *;

-- name: ListJobEvents :many
SELECT * FROM job_events
WHERE job_id = @job_id AND id > @after_id
ORDER BY id
LIMIT @row_limit;

-- name: CountJobsByState :many
SELECT state, count(*) AS count
FROM jobs
GROUP BY state
ORDER BY state;

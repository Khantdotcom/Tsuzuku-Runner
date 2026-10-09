-- +goose Up
-- Only sanity limits live here; policy limits (max CPU, memory, timeout) are
-- enforced by the API so they can change without a migration.
CREATE TABLE workloads (
    id                   uuid        PRIMARY KEY,
    idempotency_key      text        UNIQUE,
    repository_url       text        NOT NULL,
    revision             text        NOT NULL,
    command              text        NOT NULL,
    image                text        NOT NULL,
    verification_command text,
    acceptance_criteria  text[]      NOT NULL DEFAULT '{}',
    cpu_millis           integer     NOT NULL CHECK (cpu_millis > 0),
    memory_mb            integer     NOT NULL CHECK (memory_mb > 0),
    timeout_seconds      integer     NOT NULL CHECK (timeout_seconds > 0),
    network_enabled      boolean     NOT NULL DEFAULT true,
    spec                 jsonb       NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE jobs (
    id                  uuid        PRIMARY KEY,
    number              bigint      NOT NULL GENERATED ALWAYS AS IDENTITY UNIQUE,
    workload_id         uuid        NOT NULL REFERENCES workloads (id),
    state               text        NOT NULL DEFAULT 'QUEUED' CHECK (state IN (
                            'QUEUED', 'SCHEDULED', 'PREPARING', 'EXECUTING', 'VERIFYING',
                            'COMPLETED', 'FAILED', 'RETRYING', 'REPAIRING', 'BLOCKED', 'CANCELLED')),
    assigned_worker_id  uuid        REFERENCES workers (id),
    -- Earliest time the scheduler may place the job; NULL means as soon as possible.
    scheduled_at        timestamptz,
    cancel_requested_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    started_at          timestamptz,
    finished_at         timestamptz
);

CREATE INDEX jobs_queue_idx ON jobs (state, created_at);
CREATE INDEX jobs_assigned_worker_idx ON jobs (assigned_worker_id, state)
    WHERE assigned_worker_id IS NOT NULL;
CREATE INDEX jobs_workload_idx ON jobs (workload_id);

-- Append-only history of every job state change.
CREATE TABLE state_transitions (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id     uuid        NOT NULL REFERENCES jobs (id),
    -- NULL for the transition that creates the job.
    from_state text        CHECK (from_state IN (
                   'QUEUED', 'SCHEDULED', 'PREPARING', 'EXECUTING', 'VERIFYING',
                   'COMPLETED', 'FAILED', 'RETRYING', 'REPAIRING', 'BLOCKED', 'CANCELLED')),
    to_state   text        NOT NULL CHECK (to_state IN (
                   'QUEUED', 'SCHEDULED', 'PREPARING', 'EXECUTING', 'VERIFYING',
                   'COMPLETED', 'FAILED', 'RETRYING', 'REPAIRING', 'BLOCKED', 'CANCELLED')),
    actor      text        NOT NULL,
    reason     text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX state_transitions_job_idx ON state_transitions (job_id, id);

-- +goose Down
DROP TABLE state_transitions;
DROP TABLE jobs;
DROP TABLE workloads;

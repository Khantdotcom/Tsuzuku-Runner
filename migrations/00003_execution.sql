-- +goose Up
-- Attempts are never overwritten: a retry or reassignment adds a new row.
CREATE TABLE job_attempts (
    id             uuid        PRIMARY KEY,
    job_id         uuid        NOT NULL REFERENCES jobs (id),
    attempt_number integer     NOT NULL CHECK (attempt_number > 0),
    worker_id      uuid        NOT NULL REFERENCES workers (id),
    status         text        NOT NULL DEFAULT 'RUNNING' CHECK (status IN (
                       'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELLED', 'ABANDONED')),
    exit_code      integer,
    error          text,
    started_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz,
    UNIQUE (job_id, attempt_number)
);

CREATE INDEX job_attempts_worker_idx ON job_attempts (worker_id, status);

-- Timeline feed for the UI and SSE streams.
CREATE TABLE job_events (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id     uuid        NOT NULL REFERENCES jobs (id),
    attempt_id uuid        REFERENCES job_attempts (id),
    type       text        NOT NULL,
    payload    jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX job_events_job_idx ON job_events (job_id, id);

-- One row per container an attempt used: repository checkout, execution, verification.
CREATE TABLE runtimes (
    id              uuid        PRIMARY KEY,
    attempt_id      uuid        NOT NULL REFERENCES job_attempts (id),
    kind            text        NOT NULL CHECK (kind IN ('docker')),
    role            text        NOT NULL CHECK (role IN ('prepare', 'execute', 'verify')),
    image           text        NOT NULL,
    container_id    text,
    volume_name     text,
    cpu_millis      integer     NOT NULL CHECK (cpu_millis > 0),
    memory_mb       integer     NOT NULL CHECK (memory_mb > 0),
    network_enabled boolean     NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    destroyed_at    timestamptz
);

CREATE INDEX runtimes_attempt_idx ON runtimes (attempt_id);

CREATE TABLE leases (
    id          uuid        PRIMARY KEY,
    job_id      uuid        NOT NULL REFERENCES jobs (id),
    attempt_id  uuid        NOT NULL REFERENCES job_attempts (id),
    worker_id   uuid        NOT NULL REFERENCES workers (id),
    acquired_at timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    released_at timestamptz,
    CHECK (expires_at > acquired_at)
);

CREATE UNIQUE INDEX leases_one_active_per_job_idx ON leases (job_id) WHERE released_at IS NULL;
CREATE INDEX leases_active_expiry_idx ON leases (expires_at) WHERE released_at IS NULL;

-- Raw output bytes, which are not guaranteed to be valid UTF-8. seq orders
-- chunks across both streams and makes re-sent batches idempotent.
CREATE TABLE log_chunks (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id     uuid        NOT NULL REFERENCES jobs (id),
    attempt_id uuid        NOT NULL REFERENCES job_attempts (id),
    seq        integer     NOT NULL CHECK (seq >= 0),
    stream     text        NOT NULL CHECK (stream IN ('stdout', 'stderr')),
    data       bytea       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (attempt_id, seq)
);

CREATE INDEX log_chunks_job_idx ON log_chunks (job_id, id);

CREATE TABLE artifacts (
    id           uuid        PRIMARY KEY,
    job_id       uuid        NOT NULL REFERENCES jobs (id),
    attempt_id   uuid        REFERENCES job_attempts (id),
    name         text        NOT NULL,
    content_type text        NOT NULL DEFAULT 'application/octet-stream',
    storage_key  text        NOT NULL UNIQUE,
    size_bytes   bigint      NOT NULL CHECK (size_bytes >= 0),
    sha256       text        NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (job_id, attempt_id, name)
);

-- +goose Down
DROP TABLE artifacts;
DROP TABLE log_chunks;
DROP TABLE leases;
DROP TABLE runtimes;
DROP TABLE job_events;
DROP TABLE job_attempts;

-- +goose Up
CREATE TABLE verification_runs (
    id          uuid        PRIMARY KEY,
    job_id      uuid        NOT NULL REFERENCES jobs (id),
    attempt_id  uuid        NOT NULL REFERENCES job_attempts (id),
    status      text        NOT NULL DEFAULT 'RUNNING' CHECK (status IN (
                    'RUNNING', 'PASSED', 'FAILED', 'ERROR')),
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

CREATE INDEX verification_runs_job_idx ON verification_runs (job_id);

CREATE TABLE verification_checks (
    id                  uuid        PRIMARY KEY,
    verification_run_id uuid        NOT NULL REFERENCES verification_runs (id),
    name                text        NOT NULL,
    kind                text        NOT NULL CHECK (kind IN ('exit_code', 'command')),
    command             text,
    status              text        NOT NULL DEFAULT 'PENDING' CHECK (status IN (
                            'PENDING', 'RUNNING', 'PASSED', 'FAILED', 'ERROR', 'SKIPPED')),
    exit_code           integer,
    -- Tail of the check's output, kept short for display; full logs are artifacts.
    output              text        NOT NULL DEFAULT '',
    started_at          timestamptz,
    finished_at         timestamptz,
    UNIQUE (verification_run_id, name)
);

CREATE TABLE failures (
    id         uuid        PRIMARY KEY,
    job_id     uuid        NOT NULL REFERENCES jobs (id),
    attempt_id uuid        REFERENCES job_attempts (id),
    category   text        NOT NULL CHECK (category IN (
                   'REQUIREMENT', 'EXECUTOR', 'CODE', 'TEST', 'DEPENDENCY', 'ENVIRONMENT',
                   'RESOURCE', 'INFRASTRUCTURE', 'NETWORK', 'TIMEOUT', 'SECURITY', 'UNKNOWN')),
    message    text        NOT NULL,
    details    jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX failures_job_idx ON failures (job_id);

CREATE TABLE deliveries (
    id              uuid        PRIMARY KEY,
    job_id          uuid        NOT NULL REFERENCES jobs (id),
    kind            text        NOT NULL CHECK (kind IN (
                        'PATCH', 'ARTIFACT', 'GITHUB_PR', 'GITLAB_MR', 'DEPLOYMENT', 'TASK_TRACKER')),
    status          text        NOT NULL DEFAULT 'PENDING' CHECK (status IN (
                        'PENDING', 'IN_PROGRESS', 'DELIVERED', 'FAILED')),
    idempotency_key text        NOT NULL UNIQUE,
    target          text        NOT NULL DEFAULT '',
    external_ref    text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX deliveries_job_idx ON deliveries (job_id);

-- +goose Down
DROP TABLE deliveries;
DROP TABLE failures;
DROP TABLE verification_checks;
DROP TABLE verification_runs;

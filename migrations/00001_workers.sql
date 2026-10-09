-- +goose Up
-- Worker liveness is derived from last_heartbeat_at rather than stored as a status.
CREATE TABLE workers (
    id                uuid        PRIMARY KEY,
    name              text        NOT NULL UNIQUE,
    slots             integer     NOT NULL CHECK (slots > 0),
    cpu_millis        integer     NOT NULL CHECK (cpu_millis > 0),
    memory_mb         integer     NOT NULL CHECK (memory_mb > 0),
    metadata          jsonb       NOT NULL DEFAULT '{}',
    registered_at     timestamptz NOT NULL DEFAULT now(),
    last_heartbeat_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE workers;

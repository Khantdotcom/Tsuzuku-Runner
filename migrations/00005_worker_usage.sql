-- +goose Up
-- Host usage reported by the most recent heartbeat; NULL until the first one.
ALTER TABLE workers
    ADD COLUMN cpu_used_percent double precision CHECK (cpu_used_percent BETWEEN 0 AND 100),
    ADD COLUMN memory_used_mb   integer          CHECK (memory_used_mb >= 0);

-- +goose Down
ALTER TABLE workers
    DROP COLUMN memory_used_mb,
    DROP COLUMN cpu_used_percent;

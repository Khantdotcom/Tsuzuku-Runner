-- +goose Up
ALTER TABLE jobs
    ADD COLUMN priority integer NOT NULL DEFAULT 0
        CHECK (priority >= 0 AND priority <= 100);

-- Queue order: higher priority first, then oldest.
CREATE INDEX jobs_queue_priority_idx ON jobs (priority DESC, created_at, number)
    WHERE state = 'QUEUED';

-- +goose Down
DROP INDEX jobs_queue_priority_idx;
ALTER TABLE jobs DROP COLUMN priority;

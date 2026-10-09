-- Example history for local development: one COMPLETED, one FAILED, and one
-- CANCELLED job. IDs are fixed; timestamps are relative to the time of seeding.

INSERT INTO workers (id, name, slots, cpu_millis, memory_mb, metadata, registered_at, last_heartbeat_at) VALUES
    ('0192a000-0000-7000-8000-000000000001', 'seed-worker', 2, 4000, 8192,
     '{"seed": true, "os": "linux", "arch": "amd64"}',
     now() - interval '1 day', now() - interval '50 minutes');

INSERT INTO workloads (id, repository_url, revision, command, image, verification_command, acceptance_criteria,
                       cpu_millis, memory_mb, timeout_seconds, network_enabled, spec, created_at) VALUES
    ('0192a000-0000-7000-8000-000000000101', 'https://github.com/Khantdotcom/tsuzuku-sample-go', 'main',
     'go test -v ./...', 'golang:1.27', 'go vet ./...', ARRAY['all tests pass'],
     1000, 1024, 600, true,
     '{"repository": "https://github.com/Khantdotcom/tsuzuku-sample-go", "revision": "main", "command": "go test -v ./...", "runtime": {"image": "golang:1.27", "network": true}, "resources": {"cpu": 1, "memory_mb": 1024}, "timeout_seconds": 600, "acceptance_criteria": ["all tests pass"], "verification": {"command": "go vet ./..."}}',
     now() - interval '10800 seconds'),
    ('0192a000-0000-7000-8000-000000000102', 'https://github.com/Khantdotcom/tsuzuku-sample-go', 'failing',
     'go test -v ./...', 'golang:1.27', 'go vet ./...', ARRAY['all tests pass', 'do not modify existing tests'],
     1000, 1024, 600, true,
     '{"repository": "https://github.com/Khantdotcom/tsuzuku-sample-go", "revision": "failing", "command": "go test -v ./...", "runtime": {"image": "golang:1.27", "network": true}, "resources": {"cpu": 1, "memory_mb": 1024}, "timeout_seconds": 600, "acceptance_criteria": ["all tests pass", "do not modify existing tests"], "verification": {"command": "go vet ./..."}}',
     now() - interval '7200 seconds'),
    ('0192a000-0000-7000-8000-000000000103', 'https://github.com/Khantdotcom/tsuzuku-sample-go', 'main',
     'go test -race ./...', 'golang:1.27', NULL, ARRAY['race detector reports no races'],
     2000, 2048, 900, false,
     '{"repository": "https://github.com/Khantdotcom/tsuzuku-sample-go", "revision": "main", "command": "go test -race ./...", "runtime": {"image": "golang:1.27", "network": false}, "resources": {"cpu": 2, "memory_mb": 2048}, "timeout_seconds": 900, "acceptance_criteria": ["race detector reports no races"]}',
     now() - interval '3600 seconds');

INSERT INTO jobs (id, workload_id, state, assigned_worker_id, cancel_requested_at, created_at, updated_at, started_at, finished_at) VALUES
    ('0192a000-0000-7000-8000-000000000201', '0192a000-0000-7000-8000-000000000101', 'COMPLETED',
     '0192a000-0000-7000-8000-000000000001', NULL,
     now() - interval '10800 seconds', now() - interval '10740 seconds',
     now() - interval '10797 seconds', now() - interval '10740 seconds'),
    ('0192a000-0000-7000-8000-000000000202', '0192a000-0000-7000-8000-000000000102', 'FAILED',
     '0192a000-0000-7000-8000-000000000001', NULL,
     now() - interval '7200 seconds', now() - interval '7146 seconds',
     now() - interval '7197 seconds', now() - interval '7146 seconds'),
    ('0192a000-0000-7000-8000-000000000203', '0192a000-0000-7000-8000-000000000103', 'CANCELLED',
     NULL, now() - interval '3540 seconds',
     now() - interval '3600 seconds', now() - interval '3540 seconds',
     NULL, now() - interval '3540 seconds');

INSERT INTO state_transitions (job_id, from_state, to_state, actor, reason, created_at) VALUES
    ('0192a000-0000-7000-8000-000000000201', NULL,        'QUEUED',    'api',                'workload submitted',             now() - interval '10800 seconds'),
    ('0192a000-0000-7000-8000-000000000201', 'QUEUED',    'SCHEDULED', 'scheduler',          'placed on seed-worker',          now() - interval '10799 seconds'),
    ('0192a000-0000-7000-8000-000000000201', 'SCHEDULED', 'PREPARING', 'worker:seed-worker', 'attempt 1 claimed',              now() - interval '10797 seconds'),
    ('0192a000-0000-7000-8000-000000000201', 'PREPARING', 'EXECUTING', 'worker:seed-worker', 'workspace ready',                now() - interval '10790 seconds'),
    ('0192a000-0000-7000-8000-000000000201', 'EXECUTING', 'VERIFYING', 'worker:seed-worker', 'command exited with code 0',     now() - interval '10748 seconds'),
    ('0192a000-0000-7000-8000-000000000201', 'VERIFYING', 'COMPLETED', 'worker:seed-worker', 'all verification checks passed', now() - interval '10740 seconds'),
    ('0192a000-0000-7000-8000-000000000202', NULL,        'QUEUED',    'api',                'workload submitted',             now() - interval '7200 seconds'),
    ('0192a000-0000-7000-8000-000000000202', 'QUEUED',    'SCHEDULED', 'scheduler',          'placed on seed-worker',          now() - interval '7199 seconds'),
    ('0192a000-0000-7000-8000-000000000202', 'SCHEDULED', 'PREPARING', 'worker:seed-worker', 'attempt 1 claimed',              now() - interval '7197 seconds'),
    ('0192a000-0000-7000-8000-000000000202', 'PREPARING', 'EXECUTING', 'worker:seed-worker', 'workspace ready',                now() - interval '7190 seconds'),
    ('0192a000-0000-7000-8000-000000000202', 'EXECUTING', 'VERIFYING', 'worker:seed-worker', 'command exited with code 1',     now() - interval '7150 seconds'),
    ('0192a000-0000-7000-8000-000000000202', 'VERIFYING', 'FAILED',    'worker:seed-worker', 'exit code check failed',         now() - interval '7146 seconds'),
    ('0192a000-0000-7000-8000-000000000203', NULL,        'QUEUED',    'api',                'workload submitted',             now() - interval '3600 seconds'),
    ('0192a000-0000-7000-8000-000000000203', 'QUEUED',    'CANCELLED', 'api',                'cancelled before scheduling',    now() - interval '3540 seconds');

INSERT INTO job_attempts (id, job_id, attempt_number, worker_id, status, exit_code, started_at, finished_at) VALUES
    ('0192a000-0000-7000-8000-000000000301', '0192a000-0000-7000-8000-000000000201', 1,
     '0192a000-0000-7000-8000-000000000001', 'SUCCEEDED', 0,
     now() - interval '10797 seconds', now() - interval '10748 seconds'),
    ('0192a000-0000-7000-8000-000000000302', '0192a000-0000-7000-8000-000000000202', 1,
     '0192a000-0000-7000-8000-000000000001', 'FAILED', 1,
     now() - interval '7197 seconds', now() - interval '7150 seconds');

INSERT INTO job_events (job_id, attempt_id, type, payload, created_at) VALUES
    ('0192a000-0000-7000-8000-000000000201', '0192a000-0000-7000-8000-000000000301', 'attempt.started',
     '{"attempt": 1, "worker": "seed-worker"}', now() - interval '10797 seconds'),
    ('0192a000-0000-7000-8000-000000000201', '0192a000-0000-7000-8000-000000000301', 'attempt.finished',
     '{"attempt": 1, "exit_code": 0}', now() - interval '10748 seconds'),
    ('0192a000-0000-7000-8000-000000000201', '0192a000-0000-7000-8000-000000000301', 'verification.finished',
     '{"status": "PASSED"}', now() - interval '10741 seconds'),
    ('0192a000-0000-7000-8000-000000000202', '0192a000-0000-7000-8000-000000000302', 'attempt.started',
     '{"attempt": 1, "worker": "seed-worker"}', now() - interval '7197 seconds'),
    ('0192a000-0000-7000-8000-000000000202', '0192a000-0000-7000-8000-000000000302', 'attempt.finished',
     '{"attempt": 1, "exit_code": 1}', now() - interval '7150 seconds'),
    ('0192a000-0000-7000-8000-000000000202', '0192a000-0000-7000-8000-000000000302', 'verification.finished',
     '{"status": "FAILED"}', now() - interval '7147 seconds'),
    ('0192a000-0000-7000-8000-000000000203', NULL, 'job.cancel_requested',
     '{"requested_by": "user"}', now() - interval '3540 seconds');

INSERT INTO runtimes (id, attempt_id, kind, role, image, container_id, volume_name, cpu_millis, memory_mb,
                      network_enabled, created_at, destroyed_at) VALUES
    ('0192a000-0000-7000-8000-000000000401', '0192a000-0000-7000-8000-000000000301', 'docker', 'prepare',
     'alpine/git', 'seed00000401', 'tsuzuku-ws-seed-301', 1000, 1024, true,
     now() - interval '10797 seconds', now() - interval '10790 seconds'),
    ('0192a000-0000-7000-8000-000000000402', '0192a000-0000-7000-8000-000000000301', 'docker', 'execute',
     'golang:1.27', 'seed00000402', 'tsuzuku-ws-seed-301', 1000, 1024, true,
     now() - interval '10790 seconds', now() - interval '10748 seconds'),
    ('0192a000-0000-7000-8000-000000000403', '0192a000-0000-7000-8000-000000000301', 'docker', 'verify',
     'golang:1.27', 'seed00000403', 'tsuzuku-ws-seed-301', 1000, 1024, true,
     now() - interval '10748 seconds', now() - interval '10741 seconds'),
    ('0192a000-0000-7000-8000-000000000404', '0192a000-0000-7000-8000-000000000302', 'docker', 'prepare',
     'alpine/git', 'seed00000404', 'tsuzuku-ws-seed-302', 1000, 1024, true,
     now() - interval '7197 seconds', now() - interval '7190 seconds'),
    ('0192a000-0000-7000-8000-000000000405', '0192a000-0000-7000-8000-000000000302', 'docker', 'execute',
     'golang:1.27', 'seed00000405', 'tsuzuku-ws-seed-302', 1000, 1024, true,
     now() - interval '7190 seconds', now() - interval '7150 seconds'),
    ('0192a000-0000-7000-8000-000000000406', '0192a000-0000-7000-8000-000000000302', 'docker', 'verify',
     'golang:1.27', 'seed00000406', 'tsuzuku-ws-seed-302', 1000, 1024, true,
     now() - interval '7150 seconds', now() - interval '7147 seconds');

INSERT INTO leases (id, job_id, attempt_id, worker_id, acquired_at, expires_at, released_at) VALUES
    ('0192a000-0000-7000-8000-000000000501', '0192a000-0000-7000-8000-000000000201',
     '0192a000-0000-7000-8000-000000000301', '0192a000-0000-7000-8000-000000000001',
     now() - interval '10797 seconds', now() - interval '10737 seconds', now() - interval '10740 seconds'),
    ('0192a000-0000-7000-8000-000000000502', '0192a000-0000-7000-8000-000000000202',
     '0192a000-0000-7000-8000-000000000302', '0192a000-0000-7000-8000-000000000001',
     now() - interval '7197 seconds', now() - interval '7137 seconds', now() - interval '7146 seconds');

INSERT INTO log_chunks (job_id, attempt_id, seq, stream, data, created_at) VALUES
    ('0192a000-0000-7000-8000-000000000201', '0192a000-0000-7000-8000-000000000301', 0, 'stdout',
     convert_to(E'=== RUN   TestAdd\n--- PASS: TestAdd (0.00s)\n=== RUN   TestDivide\n--- PASS: TestDivide (0.00s)\nPASS\n', 'UTF8'),
     now() - interval '10760 seconds'),
    ('0192a000-0000-7000-8000-000000000201', '0192a000-0000-7000-8000-000000000301', 1, 'stdout',
     convert_to(E'ok  \tgithub.com/Khantdotcom/tsuzuku-sample-go\t0.004s\n', 'UTF8'),
     now() - interval '10749 seconds'),
    ('0192a000-0000-7000-8000-000000000202', '0192a000-0000-7000-8000-000000000302', 0, 'stdout',
     convert_to(E'=== RUN   TestAdd\n--- PASS: TestAdd (0.00s)\n=== RUN   TestDivide\n    calc_test.go:21: Divide(6, 3) = 3, want 2\n--- FAIL: TestDivide (0.00s)\nFAIL\n', 'UTF8'),
     now() - interval '7160 seconds'),
    ('0192a000-0000-7000-8000-000000000202', '0192a000-0000-7000-8000-000000000302', 1, 'stdout',
     convert_to(E'FAIL\tgithub.com/Khantdotcom/tsuzuku-sample-go\t0.005s\nFAIL\n', 'UTF8'),
     now() - interval '7151 seconds');

-- Log artifacts describe the concatenated stdout of each attempt.
INSERT INTO artifacts (id, job_id, attempt_id, name, content_type, storage_key, size_bytes, sha256, created_at)
SELECT a.id, a.job_id, a.attempt_id, 'stdout.log', 'text/plain; charset=utf-8', a.storage_key,
       octet_length(c.data), encode(sha256(c.data), 'hex'), a.created_at
FROM (VALUES
    ('0192a000-0000-7000-8000-000000000601'::uuid, '0192a000-0000-7000-8000-000000000201'::uuid,
     '0192a000-0000-7000-8000-000000000301'::uuid, 'jobs/0192a000-0000-7000-8000-000000000201/attempts/1/stdout.log',
     now() - interval '10741 seconds'),
    ('0192a000-0000-7000-8000-000000000602'::uuid, '0192a000-0000-7000-8000-000000000202'::uuid,
     '0192a000-0000-7000-8000-000000000302'::uuid, 'jobs/0192a000-0000-7000-8000-000000000202/attempts/1/stdout.log',
     now() - interval '7147 seconds')
) AS a (id, job_id, attempt_id, storage_key, created_at)
CROSS JOIN LATERAL (
    SELECT string_agg(l.data, ''::bytea ORDER BY l.seq) AS data
    FROM log_chunks l
    WHERE l.attempt_id = a.attempt_id AND l.stream = 'stdout'
) AS c;

INSERT INTO verification_runs (id, job_id, attempt_id, status, started_at, finished_at) VALUES
    ('0192a000-0000-7000-8000-000000000701', '0192a000-0000-7000-8000-000000000201',
     '0192a000-0000-7000-8000-000000000301', 'PASSED',
     now() - interval '10748 seconds', now() - interval '10741 seconds'),
    ('0192a000-0000-7000-8000-000000000702', '0192a000-0000-7000-8000-000000000202',
     '0192a000-0000-7000-8000-000000000302', 'FAILED',
     now() - interval '7150 seconds', now() - interval '7147 seconds');

INSERT INTO verification_checks (id, verification_run_id, name, kind, command, status, exit_code, output,
                                 started_at, finished_at) VALUES
    ('0192a000-0000-7000-8000-000000000801', '0192a000-0000-7000-8000-000000000701',
     'exit code is 0', 'exit_code', NULL, 'PASSED', 0, '',
     now() - interval '10748 seconds', now() - interval '10748 seconds'),
    ('0192a000-0000-7000-8000-000000000802', '0192a000-0000-7000-8000-000000000701',
     'go vet ./...', 'command', 'go vet ./...', 'PASSED', 0, '',
     now() - interval '10747 seconds', now() - interval '10741 seconds'),
    ('0192a000-0000-7000-8000-000000000803', '0192a000-0000-7000-8000-000000000702',
     'exit code is 0', 'exit_code', NULL, 'FAILED', 1, 'command exited with code 1',
     now() - interval '7150 seconds', now() - interval '7150 seconds'),
    ('0192a000-0000-7000-8000-000000000804', '0192a000-0000-7000-8000-000000000702',
     'go vet ./...', 'command', 'go vet ./...', 'SKIPPED', NULL, 'skipped because an earlier check failed',
     NULL, NULL);

INSERT INTO failures (id, job_id, attempt_id, category, message, details, created_at) VALUES
    ('0192a000-0000-7000-8000-000000000901', '0192a000-0000-7000-8000-000000000202',
     '0192a000-0000-7000-8000-000000000302', 'TEST',
     'go test -v ./... exited with code 1: TestDivide failed',
     '{"exit_code": 1, "failed_tests": ["TestDivide"]}', now() - interval '7147 seconds');

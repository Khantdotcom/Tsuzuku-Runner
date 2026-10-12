-- name: InsertDelivery :one
-- InsertDelivery records a new outbound delivery. A conflict on the
-- idempotency key returns no row; callers look up the existing delivery.
INSERT INTO deliveries (id, job_id, kind, status, idempotency_key, target)
VALUES (@id, @job_id, @kind, 'PENDING', @idempotency_key, @target)
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING *;

-- name: GetDeliveryByKey :one
SELECT * FROM deliveries
WHERE idempotency_key = @idempotency_key;

-- name: GetDelivery :one
SELECT * FROM deliveries
WHERE id = @id;

-- name: ListDeliveriesByJob :many
SELECT * FROM deliveries
WHERE job_id = @job_id
ORDER BY created_at, kind;

-- name: ClaimDelivery :one
-- ClaimDelivery moves a delivery into IN_PROGRESS so only one attempt runs.
-- PENDING, FAILED, and abandoned IN_PROGRESS rows may be claimed again.
UPDATE deliveries
SET status = 'IN_PROGRESS', updated_at = now()
WHERE id = @id AND status IN ('PENDING', 'FAILED', 'IN_PROGRESS')
RETURNING *;

-- name: CompleteDelivery :one
UPDATE deliveries
SET status = 'DELIVERED',
    external_ref = @external_ref,
    target = CASE WHEN @set_target::boolean THEN @target ELSE target END,
    updated_at = now()
WHERE id = @id AND status = 'IN_PROGRESS'
RETURNING *;

-- name: FailDelivery :one
UPDATE deliveries
SET status = 'FAILED',
    external_ref = @external_ref,
    updated_at = now()
WHERE id = @id AND status = 'IN_PROGRESS'
RETURNING *;

-- name: RecordWebhookDelivery :one
-- Records delivery_id as seen. Returns no row (pgx.ErrNoRows) when it was
-- already recorded -- the caller's signal that this is a redelivery.
INSERT INTO webhook_delivery (delivery_id)
VALUES ($1)
ON CONFLICT DO NOTHING
RETURNING *;

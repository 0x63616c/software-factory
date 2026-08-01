-- +goose Up
-- The factory's webhook consumer idempotency key (#557). It is deliberately
-- NOT a verbatim archive of the delivery: control-center's incoming_webhook
-- already holds that, and this consumer acts rather than hoards. One row per
-- delivery this consumer actually acted on, keyed on GitHub's own delivery
-- id, so GitHub's "Redeliver" button and the relay's own retries are safe.
CREATE TABLE webhook_delivery (
    delivery_id TEXT PRIMARY KEY,
    received_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE webhook_delivery;

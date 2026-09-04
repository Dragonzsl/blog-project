-- +goose Up

ALTER TABLE notification_outbox ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT '';
ALTER TABLE notification_outbox ADD COLUMN lease_expires_at INTEGER;
ALTER TABLE notification_outbox ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
UPDATE notification_outbox SET updated_at = created_at WHERE updated_at = 0;

CREATE UNIQUE INDEX notification_outbox_idempotency_idx
    ON notification_outbox(idempotency_key) WHERE idempotency_key <> '';
CREATE INDEX notification_outbox_claim_idx
    ON notification_outbox(status, available_at, lease_expires_at, id);

-- Stable event scope makes duplicate dispatches converge on one delivery.
ALTER TABLE webhook_deliveries ADD COLUMN event_key TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX webhook_deliveries_event_idx
    ON webhook_deliveries(event_key) WHERE event_key <> '';

CREATE INDEX jobs_claim_idx ON jobs(status, available_at, attempts, id);

-- Rows written by pre-phase-one versions did not have a corresponding core
-- task. Backfill only pending notifications; sent and terminal rows remain
-- historical records and must not be delivered again.
INSERT INTO jobs(kind,payload_version,payload,idempotency_key,status,available_at,attempts,created_at,updated_at)
SELECT 'core:notification_send', 1,
       CAST('{"notification_id":' || id || '}' AS BLOB),
       'notification:' || id, 'pending', available_at, 0, created_at, updated_at
FROM notification_outbox
WHERE status='pending'
ON CONFLICT(idempotency_key) DO NOTHING;

-- +goose Down
DROP INDEX jobs_claim_idx;
DROP INDEX webhook_deliveries_event_idx;
ALTER TABLE webhook_deliveries DROP COLUMN event_key;
DROP INDEX notification_outbox_claim_idx;
DROP INDEX notification_outbox_idempotency_idx;
ALTER TABLE notification_outbox DROP COLUMN updated_at;
ALTER TABLE notification_outbox DROP COLUMN lease_expires_at;
ALTER TABLE notification_outbox DROP COLUMN idempotency_key;

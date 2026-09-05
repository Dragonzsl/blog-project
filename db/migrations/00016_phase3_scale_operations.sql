-- +goose Up

-- The task row remains the durable source of truth. These nullable fields
-- describe only the latest attempt and do not create an unbounded history.
ALTER TABLE jobs ADD COLUMN last_started_at INTEGER;
ALTER TABLE jobs ADD COLUMN last_completed_at INTEGER;
ALTER TABLE jobs ADD COLUMN last_duration_ms INTEGER
    CHECK (last_duration_ms IS NULL OR last_duration_ms >= 0);
CREATE INDEX jobs_observability_idx ON jobs(status, updated_at DESC, id);

-- Storage migration progress is advanced only after one source object has
-- passed source and destination checksum verification. A failed invocation
-- can therefore resume from the last confirmed cursor without retaining all
-- media objects in memory.
ALTER TABLE storage_migrations ADD COLUMN cursor_media_id INTEGER NOT NULL DEFAULT 0 CHECK (cursor_media_id >= 0);
ALTER TABLE storage_migrations ADD COLUMN cursor_variant_key TEXT NOT NULL DEFAULT '';
ALTER TABLE storage_migrations ADD COLUMN batch_size INTEGER NOT NULL DEFAULT 16 CHECK (batch_size > 0);
ALTER TABLE storage_migrations ADD COLUMN lease_expires_at INTEGER;
CREATE INDEX storage_migrations_latest_idx ON storage_migrations(source_adapter, destination_adapter, status, started_at DESC, id DESC);

ALTER TABLE backups ADD COLUMN object_key TEXT NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN verified_at INTEGER;
ALTER TABLE backups ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
CREATE INDEX backups_verified_idx ON backups(checksum_status, verified_at DESC, created_at DESC);

-- +goose Down
DROP INDEX storage_migrations_latest_idx;
DROP INDEX backups_verified_idx;
ALTER TABLE backups DROP COLUMN last_error;
ALTER TABLE backups DROP COLUMN verified_at;
ALTER TABLE backups DROP COLUMN object_key;
ALTER TABLE storage_migrations DROP COLUMN lease_expires_at;
ALTER TABLE storage_migrations DROP COLUMN batch_size;
ALTER TABLE storage_migrations DROP COLUMN cursor_variant_key;
ALTER TABLE storage_migrations DROP COLUMN cursor_media_id;
DROP INDEX jobs_observability_idx;
ALTER TABLE jobs DROP COLUMN last_duration_ms;
ALTER TABLE jobs DROP COLUMN last_completed_at;
ALTER TABLE jobs DROP COLUMN last_started_at;

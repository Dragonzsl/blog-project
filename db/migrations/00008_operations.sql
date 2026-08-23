-- +goose Up
ALTER TABLE backups ADD COLUMN reason TEXT NOT NULL DEFAULT 'manual'
    CHECK (reason IN ('manual', 'scheduled', 'pre_upgrade'));
ALTER TABLE backups ADD COLUMN application_version TEXT NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN migration_version INTEGER NOT NULL DEFAULT 0 CHECK (migration_version >= 0);

CREATE INDEX backups_retention_idx ON backups(reason, created_at DESC);

-- +goose Down
DROP INDEX backups_retention_idx;
ALTER TABLE backups DROP COLUMN migration_version;
ALTER TABLE backups DROP COLUMN application_version;
ALTER TABLE backups DROP COLUMN reason;

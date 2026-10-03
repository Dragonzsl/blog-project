-- +goose Up
ALTER TABLE plugin_states ADD COLUMN removed_at INTEGER
    CHECK (removed_at IS NULL OR (removed_at >= 0 AND enabled = 0));

-- +goose Down
ALTER TABLE plugin_states DROP COLUMN removed_at;

-- +goose Up
-- Existing installations have only the package checksum. New installs also
-- record the deterministic extracted-directory checksum so activation can
-- detect tampering or partial replacement before changing the active theme.
ALTER TABLE themes ADD COLUMN directory_checksum BLOB
    CHECK (directory_checksum IS NULL OR length(directory_checksum) = 32);

CREATE TABLE theme_activation_history (
    id INTEGER PRIMARY KEY,
    previous_theme_id INTEGER REFERENCES themes(id) ON DELETE SET NULL,
    active_theme_id INTEGER REFERENCES themes(id) ON DELETE SET NULL,
    operation TEXT NOT NULL CHECK (operation IN ('activate', 'rollback')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'succeeded', 'failed')),
    error_summary TEXT NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL,
    completed_at INTEGER
) STRICT;

CREATE INDEX theme_activation_history_latest_idx ON theme_activation_history(id DESC);

-- +goose Down
DROP INDEX theme_activation_history_latest_idx;
DROP TABLE theme_activation_history;
ALTER TABLE themes DROP COLUMN directory_checksum;

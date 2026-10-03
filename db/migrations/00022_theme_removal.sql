-- +goose Up
ALTER TABLE themes ADD COLUMN removed_at INTEGER
    CHECK (removed_at IS NULL OR (removed_at >= 0 AND active = 0));

-- +goose Down
ALTER TABLE themes DROP COLUMN removed_at;

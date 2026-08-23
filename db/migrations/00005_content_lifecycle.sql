-- +goose Up
CREATE TABLE editing_snapshots (
    content_id INTEGER PRIMARY KEY REFERENCES contents(id) ON DELETE CASCADE,
    base_lock_version INTEGER NOT NULL CHECK (base_lock_version > 0),
    browser_version INTEGER NOT NULL CHECK (browser_version > 0),
    title TEXT NOT NULL,
    slug TEXT NOT NULL,
    excerpt TEXT NOT NULL,
    body_markdown TEXT NOT NULL,
    category_id INTEGER REFERENCES categories(id) ON DELETE SET NULL,
    tag_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tag_ids_json)),
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX contents_schedule_idx ON contents (status, scheduled_at, id)
WHERE status = 'scheduled' AND trashed_at IS NULL;

CREATE INDEX contents_trash_idx ON contents (trashed_at, id)
WHERE trashed_at IS NOT NULL;

-- +goose Down
DROP INDEX contents_trash_idx;
DROP INDEX contents_schedule_idx;
DROP TABLE editing_snapshots;

-- +goose Up
-- A revision stores the public identity of its cover, instead of the mutable
-- media row id.  Version zero means that the revision predates cover
-- snapshots; restoring such a revision must preserve the current cover.
ALTER TABLE content_revisions ADD COLUMN cover_media_public_id BLOB
    CHECK (cover_media_public_id IS NULL OR length(cover_media_public_id) = 16);
ALTER TABLE content_revisions ADD COLUMN cover_snapshot_version INTEGER NOT NULL DEFAULT 0
    CHECK (cover_snapshot_version >= 0);

-- Editing snapshots are working state rather than formal revisions, so they
-- keep the local media row id used by the admin picker.
ALTER TABLE editing_snapshots ADD COLUMN cover_media_id INTEGER
    REFERENCES media(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE editing_snapshots DROP COLUMN cover_media_id;
ALTER TABLE content_revisions DROP COLUMN cover_snapshot_version;
ALTER TABLE content_revisions DROP COLUMN cover_media_public_id;

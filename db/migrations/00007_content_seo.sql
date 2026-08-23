-- +goose Up
ALTER TABLE contents ADD COLUMN seo_title TEXT NOT NULL DEFAULT '';
ALTER TABLE contents ADD COLUMN seo_description TEXT NOT NULL DEFAULT '';
ALTER TABLE content_revisions ADD COLUMN seo_title TEXT NOT NULL DEFAULT '';
ALTER TABLE content_revisions ADD COLUMN seo_description TEXT NOT NULL DEFAULT '';
ALTER TABLE editing_snapshots ADD COLUMN seo_title TEXT NOT NULL DEFAULT '';
ALTER TABLE editing_snapshots ADD COLUMN seo_description TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE editing_snapshots DROP COLUMN seo_description;
ALTER TABLE editing_snapshots DROP COLUMN seo_title;
ALTER TABLE content_revisions DROP COLUMN seo_description;
ALTER TABLE content_revisions DROP COLUMN seo_title;
ALTER TABLE contents DROP COLUMN seo_description;
ALTER TABLE contents DROP COLUMN seo_title;

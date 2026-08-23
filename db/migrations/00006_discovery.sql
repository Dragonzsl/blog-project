-- +goose Up
ALTER TABLE contents ADD COLUMN published_slug TEXT;
ALTER TABLE contents ADD COLUMN published_slug_key TEXT;

UPDATE contents
SET published_slug = slug,
    published_slug_key = slug_key
WHERE published_revision_id IS NOT NULL;

CREATE UNIQUE INDEX contents_published_slug_idx
ON contents(kind, published_slug_key)
WHERE published_slug_key IS NOT NULL;

CREATE TABLE redirects (
    id INTEGER PRIMARY KEY,
    source_path TEXT NOT NULL,
    source_path_key TEXT NOT NULL UNIQUE,
    target_path TEXT NOT NULL,
    target_path_key TEXT NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 301 CHECK (status_code IN (301, 308)),
    reason TEXT NOT NULL CHECK (reason IN ('content_slug', 'category_slug', 'tag_slug', 'manual')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK (source_path_key != target_path_key)
) STRICT;

CREATE VIRTUAL TABLE search_documents USING fts5(
    kind UNINDEXED,
    path UNINDEXED,
    published_at UNINDEXED,
    category_slug UNINDEXED,
    tag_slugs UNINDEXED,
    title,
    excerpt,
    body,
    taxonomy,
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TABLE search_grams (
    content_id INTEGER NOT NULL,
    gram TEXT NOT NULL,
    PRIMARY KEY(content_id, gram)
) WITHOUT ROWID, STRICT;

CREATE INDEX search_grams_gram_idx ON search_grams(gram, content_id);

CREATE TABLE search_dirty (
    content_id INTEGER PRIMARY KEY,
    queued_at INTEGER NOT NULL
) STRICT;

INSERT INTO search_dirty(content_id, queued_at)
SELECT id, unixepoch('subsec') * 1000 FROM contents;

-- +goose StatementBegin
CREATE TRIGGER contents_search_dirty_insert
AFTER INSERT ON contents
BEGIN
    INSERT INTO search_dirty(content_id, queued_at)
    VALUES(NEW.id, unixepoch('subsec') * 1000)
    ON CONFLICT(content_id) DO UPDATE SET queued_at=excluded.queued_at;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER contents_search_dirty_update
AFTER UPDATE OF status, published_revision_id, published_slug, trashed_at ON contents
BEGIN
    INSERT INTO search_dirty(content_id, queued_at)
    VALUES(NEW.id, unixepoch('subsec') * 1000)
    ON CONFLICT(content_id) DO UPDATE SET queued_at=excluded.queued_at;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER contents_search_dirty_delete
AFTER DELETE ON contents
BEGIN
    INSERT INTO search_dirty(content_id, queued_at)
    VALUES(OLD.id, unixepoch('subsec') * 1000)
    ON CONFLICT(content_id) DO UPDATE SET queued_at=excluded.queued_at;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER categories_search_dirty_update
AFTER UPDATE OF name, slug ON categories
BEGIN
    INSERT INTO search_dirty(content_id, queued_at)
    SELECT c.id, unixepoch('subsec') * 1000
    FROM contents c
    JOIN content_revisions revision ON revision.id = c.published_revision_id
    WHERE revision.category_public_id = NEW.public_id
    ON CONFLICT(content_id) DO UPDATE SET queued_at=excluded.queued_at;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tags_search_dirty_update
AFTER UPDATE OF name, slug ON tags
BEGIN
    INSERT INTO search_dirty(content_id, queued_at)
    SELECT c.id, unixepoch('subsec') * 1000
    FROM contents c
    JOIN content_revisions revision ON revision.id = c.published_revision_id
    WHERE EXISTS (
        SELECT 1 FROM json_each(revision.tag_public_ids_json)
        WHERE json_each.value = lower(hex(NEW.public_id))
    )
    ON CONFLICT(content_id) DO UPDATE SET queued_at=excluded.queued_at;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER tags_search_dirty_update;
DROP TRIGGER categories_search_dirty_update;
DROP TRIGGER contents_search_dirty_delete;
DROP TRIGGER contents_search_dirty_update;
DROP TRIGGER contents_search_dirty_insert;
DROP TABLE search_dirty;
DROP INDEX search_grams_gram_idx;
DROP TABLE search_grams;
DROP TABLE search_documents;
DROP TABLE redirects;
DROP INDEX contents_published_slug_idx;
ALTER TABLE contents DROP COLUMN published_slug_key;
ALTER TABLE contents DROP COLUMN published_slug;

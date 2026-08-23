-- +goose Up
CREATE TABLE categories (
    id INTEGER PRIMARY KEY,
    public_id BLOB NOT NULL UNIQUE CHECK (length(public_id) = 16),
    slug TEXT NOT NULL CHECK (length(slug) BETWEEN 1 AND 120),
    slug_key TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    description TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE tags (
    id INTEGER PRIMARY KEY,
    public_id BLOB NOT NULL UNIQUE CHECK (length(public_id) = 16),
    slug TEXT NOT NULL CHECK (length(slug) BETWEEN 1 AND 120),
    slug_key TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    description TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

ALTER TABLE contents ADD COLUMN category_id INTEGER REFERENCES categories(id) ON DELETE SET NULL;
ALTER TABLE contents ADD COLUMN cover_media_id INTEGER REFERENCES media(id) ON DELETE SET NULL;
ALTER TABLE content_revisions ADD COLUMN category_public_id BLOB CHECK (category_public_id IS NULL OR length(category_public_id) = 16);
ALTER TABLE content_revisions ADD COLUMN tag_public_ids_json TEXT NOT NULL DEFAULT '[]';

CREATE TABLE content_tags (
    content_id INTEGER NOT NULL REFERENCES contents(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (content_id, tag_id)
) STRICT;

CREATE INDEX content_tags_tag_idx ON content_tags (tag_id, content_id);

-- +goose StatementBegin
CREATE TRIGGER contents_page_category_insert
BEFORE INSERT ON contents
WHEN NEW.kind = 'page' AND NEW.category_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'pages cannot have categories');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER contents_page_category_update
BEFORE UPDATE OF kind, category_id ON contents
WHEN NEW.kind = 'page' AND NEW.category_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'pages cannot have categories');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER content_tags_articles_only
BEFORE INSERT ON content_tags
WHEN NOT EXISTS (SELECT 1 FROM contents WHERE id = NEW.content_id AND kind = 'article')
BEGIN
    SELECT RAISE(ABORT, 'only articles can have tags');
END;
-- +goose StatementEnd

CREATE TABLE navigation_menus (
    id INTEGER PRIMARY KEY,
    location TEXT NOT NULL UNIQUE CHECK (location IN ('primary', 'footer')),
    name TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

INSERT INTO navigation_menus (location, name, created_at, updated_at)
VALUES
    ('primary', '主导航', unixepoch('subsec') * 1000, unixepoch('subsec') * 1000),
    ('footer', '页脚导航', unixepoch('subsec') * 1000, unixepoch('subsec') * 1000);

CREATE TABLE navigation_items (
    id INTEGER PRIMARY KEY,
    menu_id INTEGER NOT NULL REFERENCES navigation_menus(id) ON DELETE CASCADE,
    parent_id INTEGER REFERENCES navigation_items(id) ON DELETE CASCADE,
    label TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 100),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('content', 'category', 'tag', 'external')),
    content_id INTEGER REFERENCES contents(id) ON DELETE CASCADE,
    category_id INTEGER REFERENCES categories(id) ON DELETE CASCADE,
    tag_id INTEGER REFERENCES tags(id) ON DELETE CASCADE,
    external_url TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK (
        (target_kind = 'content' AND content_id IS NOT NULL AND category_id IS NULL AND tag_id IS NULL AND external_url IS NULL) OR
        (target_kind = 'category' AND content_id IS NULL AND category_id IS NOT NULL AND tag_id IS NULL AND external_url IS NULL) OR
        (target_kind = 'tag' AND content_id IS NULL AND category_id IS NULL AND tag_id IS NOT NULL AND external_url IS NULL) OR
        (target_kind = 'external' AND content_id IS NULL AND category_id IS NULL AND tag_id IS NULL AND external_url IS NOT NULL)
    )
) STRICT;

CREATE INDEX navigation_items_menu_idx ON navigation_items (menu_id, sort_order, id);

-- +goose StatementBegin
CREATE TRIGGER navigation_items_one_level_insert
BEFORE INSERT ON navigation_items
WHEN NEW.parent_id IS NOT NULL AND EXISTS (
    SELECT 1 FROM navigation_items parent WHERE parent.id = NEW.parent_id AND parent.parent_id IS NOT NULL
)
BEGIN
    SELECT RAISE(ABORT, 'navigation supports one child level');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER navigation_items_one_level_update
BEFORE UPDATE OF parent_id ON navigation_items
WHEN NEW.parent_id IS NOT NULL AND (
    EXISTS (SELECT 1 FROM navigation_items parent WHERE parent.id = NEW.parent_id AND parent.parent_id IS NOT NULL) OR
    EXISTS (SELECT 1 FROM navigation_items child WHERE child.parent_id = NEW.id)
)
BEGIN
    SELECT RAISE(ABORT, 'navigation supports one child level');
END;
-- +goose StatementEnd

CREATE TABLE media (
    id INTEGER PRIMARY KEY,
    public_id BLOB NOT NULL UNIQUE CHECK (length(public_id) = 16),
    original_name TEXT NOT NULL CHECK (length(original_name) BETWEEN 1 AND 255),
    mime_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    width INTEGER CHECK (width IS NULL OR width > 0),
    height INTEGER CHECK (height IS NULL OR height > 0),
    content_hash BLOB NOT NULL CHECK (length(content_hash) = 32),
    alt_text TEXT NOT NULL DEFAULT '',
    storage_adapter TEXT NOT NULL DEFAULT 'local' CHECK (storage_adapter = 'local'),
    object_key TEXT NOT NULL UNIQUE,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX media_hash_idx ON media (content_hash);

CREATE TABLE media_variants (
    id INTEGER PRIMARY KEY,
    media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    variant_key TEXT NOT NULL,
    width INTEGER NOT NULL CHECK (width > 0),
    height INTEGER NOT NULL CHECK (height > 0),
    mime_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    content_hash BLOB NOT NULL CHECK (length(content_hash) = 32),
    object_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('ready', 'failed')),
    created_at INTEGER NOT NULL,
    UNIQUE (media_id, variant_key)
) STRICT;

CREATE TABLE media_references (
    content_id INTEGER NOT NULL REFERENCES contents(id) ON DELETE CASCADE,
    media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE RESTRICT,
    relation TEXT NOT NULL CHECK (relation IN ('body', 'cover')),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (content_id, media_id, relation)
) STRICT;

CREATE INDEX media_references_media_idx ON media_references (media_id, content_id);

INSERT INTO reserved_paths (path, path_key, content_id, reason, created_at)
VALUES
    ('/', '/', NULL, 'system', unixepoch('subsec') * 1000),
    ('/admin', '/admin', NULL, 'system', unixepoch('subsec') * 1000),
    ('/assets', '/assets', NULL, 'system', unixepoch('subsec') * 1000),
    ('/media', '/media', NULL, 'system', unixepoch('subsec') * 1000),
    ('/posts', '/posts', NULL, 'system', unixepoch('subsec') * 1000),
    ('/categories', '/categories', NULL, 'system', unixepoch('subsec') * 1000),
    ('/tags', '/tags', NULL, 'system', unixepoch('subsec') * 1000),
    ('/search', '/search', NULL, 'system', unixepoch('subsec') * 1000),
    ('/rss.xml', '/rss.xml', NULL, 'system', unixepoch('subsec') * 1000),
    ('/sitemap.xml', '/sitemap.xml', NULL, 'system', unixepoch('subsec') * 1000),
    ('/robots.txt', '/robots.txt', NULL, 'system', unixepoch('subsec') * 1000);

-- +goose Down
DELETE FROM reserved_paths WHERE reason = 'system' AND path_key IN (
    '/', '/admin', '/assets', '/media', '/posts', '/categories', '/tags',
    '/search', '/rss.xml', '/sitemap.xml', '/robots.txt'
);
DROP INDEX media_references_media_idx;
DROP TABLE media_references;
DROP TABLE media_variants;
ALTER TABLE contents DROP COLUMN cover_media_id;
DROP INDEX media_hash_idx;
DROP TABLE media;
DROP TRIGGER navigation_items_one_level_update;
DROP TRIGGER navigation_items_one_level_insert;
DROP INDEX navigation_items_menu_idx;
DROP TABLE navigation_items;
DROP TABLE navigation_menus;
DROP TRIGGER content_tags_articles_only;
DROP TRIGGER contents_page_category_update;
DROP TRIGGER contents_page_category_insert;
DROP INDEX content_tags_tag_idx;
DROP TABLE content_tags;
ALTER TABLE content_revisions DROP COLUMN tag_public_ids_json;
ALTER TABLE content_revisions DROP COLUMN category_public_id;
ALTER TABLE contents DROP COLUMN category_id;
DROP TABLE tags;
DROP TABLE categories;

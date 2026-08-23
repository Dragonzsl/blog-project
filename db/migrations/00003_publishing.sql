-- +goose Up
CREATE TABLE contents (
    id INTEGER PRIMARY KEY,
    public_id BLOB NOT NULL UNIQUE CHECK (length(public_id) = 16),
    kind TEXT NOT NULL CHECK (kind IN ('article', 'page')),
    status TEXT NOT NULL CHECK (status IN ('draft', 'scheduled', 'published')),
    slug TEXT NOT NULL CHECK (length(slug) BETWEEN 1 AND 120),
    slug_key TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    excerpt TEXT NOT NULL DEFAULT '',
    body_markdown TEXT NOT NULL DEFAULT '',
    current_revision_id INTEGER REFERENCES content_revisions(id),
    published_revision_id INTEGER REFERENCES content_revisions(id),
    published_at INTEGER,
    scheduled_at INTEGER,
    withdrawn_at INTEGER,
    trashed_at INTEGER,
    lock_version INTEGER NOT NULL DEFAULT 1 CHECK (lock_version > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK (status != 'published' OR (published_revision_id IS NOT NULL AND published_at IS NOT NULL)),
    CHECK (status != 'scheduled' OR scheduled_at IS NOT NULL)
) STRICT;

CREATE INDEX contents_publication_idx ON contents (kind, status, published_at DESC);

CREATE TABLE content_revisions (
    id INTEGER PRIMARY KEY,
    public_id BLOB NOT NULL UNIQUE CHECK (length(public_id) = 16),
    content_id INTEGER NOT NULL REFERENCES contents(id) ON DELETE CASCADE,
    revision_number INTEGER NOT NULL CHECK (revision_number > 0),
    title TEXT NOT NULL,
    slug TEXT NOT NULL,
    excerpt TEXT NOT NULL,
    body_markdown TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (reason IN ('create', 'save', 'restore', 'import')),
    is_publication_checkpoint INTEGER NOT NULL DEFAULT 0 CHECK (is_publication_checkpoint IN (0, 1)),
    created_at INTEGER NOT NULL,
    UNIQUE (content_id, revision_number)
) STRICT;

CREATE INDEX content_revisions_content_idx ON content_revisions (content_id, revision_number DESC);

CREATE TABLE reserved_paths (
    id INTEGER PRIMARY KEY,
    path TEXT NOT NULL,
    path_key TEXT NOT NULL UNIQUE,
    content_id INTEGER REFERENCES contents(id) ON DELETE SET NULL,
    reason TEXT NOT NULL CHECK (reason IN ('draft', 'published', 'historical', 'system')),
    created_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE reserved_paths;
DROP INDEX content_revisions_content_idx;
DROP TABLE content_revisions;
DROP INDEX contents_publication_idx;
DROP TABLE contents;

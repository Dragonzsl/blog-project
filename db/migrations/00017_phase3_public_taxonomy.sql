-- +goose Up

-- Rebuildable public taxonomy membership. The published revision remains the
-- source of truth; this table only removes JSON expansion from public reads.
CREATE TABLE public_taxonomy_members (
    content_id INTEGER NOT NULL REFERENCES contents(id) ON DELETE CASCADE,
    published_revision_id INTEGER NOT NULL REFERENCES content_revisions(id) ON DELETE CASCADE,
    taxonomy_kind TEXT NOT NULL CHECK (taxonomy_kind IN ('category','tag')),
    taxonomy_public_id BLOB NOT NULL,
    title TEXT NOT NULL,
    published_slug TEXT NOT NULL,
    published_at INTEGER NOT NULL,
    PRIMARY KEY (content_id, taxonomy_kind, taxonomy_public_id)
);

CREATE INDEX public_taxonomy_members_term_idx
    ON public_taxonomy_members(taxonomy_kind, taxonomy_public_id, published_at DESC, content_id DESC);
CREATE INDEX public_taxonomy_members_content_idx
    ON public_taxonomy_members(content_id, published_revision_id, taxonomy_kind);

-- A singleton state row makes a rebuild resumable without putting a large
-- backfill inside application startup or a schema migration transaction.
CREATE TABLE public_taxonomy_rebuild_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','ready','failed')),
    cursor_content_id INTEGER NOT NULL DEFAULT 0 CHECK (cursor_content_id >= 0),
    schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version > 0),
    last_error TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT 0
);

INSERT INTO public_taxonomy_rebuild_state(id) VALUES (1);

-- +goose Down
DROP TABLE public_taxonomy_rebuild_state;
DROP INDEX public_taxonomy_members_content_idx;
DROP INDEX public_taxonomy_members_term_idx;
DROP TABLE public_taxonomy_members;

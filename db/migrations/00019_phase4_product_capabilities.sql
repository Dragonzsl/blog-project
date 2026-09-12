-- +goose Up

-- Public site metadata is intentionally kept as bounded columns. Secrets and
-- provider configuration remain outside this table.
ALTER TABLE sites ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE sites ADD COLUMN default_seo_title TEXT NOT NULL DEFAULT '';
ALTER TABLE sites ADD COLUMN default_seo_description TEXT NOT NULL DEFAULT '';
ALTER TABLE sites ADD COLUMN social_links_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE sites ADD COLUMN default_social_image_public_id BLOB;

-- A challenge contains only an encrypted candidate secret. The raw challenge
-- token is never persisted and replacing a challenge invalidates the older
-- one for the same owner and purpose.
CREATE TABLE owner_security_challenges (
    id INTEGER PRIMARY KEY,
    owner_id INTEGER NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('totp_rotation')),
    token_hash BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    candidate_totp_secret_cipher BLOB NOT NULL,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE(owner_id, purpose)
) STRICT;

-- Bulk operations are durable request summaries. Item writes still happen in
-- separate short transactions in the publishing module.
CREATE TABLE bulk_operations (
    id INTEGER PRIMARY KEY,
    operation_key TEXT NOT NULL UNIQUE CHECK (length(operation_key) BETWEEN 1 AND 180),
    object_kind TEXT NOT NULL CHECK (object_kind IN ('article', 'page', 'comment')),
    action TEXT NOT NULL CHECK (length(action) BETWEEN 1 AND 40),
    input_json TEXT NOT NULL CHECK (length(input_json) <= 16384),
    status TEXT NOT NULL CHECK (status IN ('processing', 'succeeded', 'failed')),
    result_json TEXT NOT NULL DEFAULT '{}' CHECK (length(result_json) <= 16384),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX bulk_operations_status_idx ON bulk_operations(status, updated_at, id);

-- These indexes support the new bounded admin filters; public comment reads
-- continue to use the existing content/status index.
CREATE INDEX comments_admin_status_idx ON comments(status, created_at DESC, id DESC);
CREATE INDEX newsletter_subscribers_status_idx ON newsletter_subscribers(status, updated_at DESC, id DESC);

ALTER TABLE newsletter_subscribers ADD COLUMN confirmed_at INTEGER;
ALTER TABLE newsletter_subscribers ADD COLUMN provider_synced_at INTEGER;
ALTER TABLE newsletter_subscribers ADD COLUMN last_provider_error TEXT NOT NULL DEFAULT '';

-- +goose Down
DROP INDEX newsletter_subscribers_status_idx;
DROP INDEX comments_admin_status_idx;
DROP INDEX bulk_operations_status_idx;
DROP TABLE bulk_operations;
DROP TABLE owner_security_challenges;
ALTER TABLE newsletter_subscribers DROP COLUMN last_provider_error;
ALTER TABLE newsletter_subscribers DROP COLUMN provider_synced_at;
ALTER TABLE newsletter_subscribers DROP COLUMN confirmed_at;
ALTER TABLE sites DROP COLUMN default_social_image_public_id;
ALTER TABLE sites DROP COLUMN social_links_json;
ALTER TABLE sites DROP COLUMN default_seo_description;
ALTER TABLE sites DROP COLUMN default_seo_title;
ALTER TABLE sites DROP COLUMN description;

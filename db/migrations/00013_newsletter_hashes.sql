-- +goose Up

-- Version 1 rows contain the historical unkeyed email_hash. The application
-- rekeys those rows in bounded batches after startup, using the encrypted
-- address as the authoritative source. New rows use version 2 directly.
ALTER TABLE newsletter_subscribers ADD COLUMN email_hash_version INTEGER NOT NULL DEFAULT 1 CHECK (email_hash_version IN (1, 2));
CREATE INDEX newsletter_subscribers_hash_version_idx ON newsletter_subscribers(email_hash_version, id);

-- +goose Down
DROP INDEX newsletter_subscribers_hash_version_idx;
ALTER TABLE newsletter_subscribers DROP COLUMN email_hash_version;

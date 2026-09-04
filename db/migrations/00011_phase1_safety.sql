-- +goose Up

-- Public-write replay protection.  Only keyed digests and a bounded safe
-- response are retained; raw request bodies, addresses, and email addresses
-- never enter these tables.
CREATE TABLE request_idempotencies (
    id INTEGER PRIMARY KEY,
    scope TEXT NOT NULL CHECK (length(scope) BETWEEN 1 AND 80),
    key_hash BLOB NOT NULL CHECK (length(key_hash) = 32),
    request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
    status TEXT NOT NULL CHECK (status IN ('processing', 'succeeded')),
    response_status INTEGER NOT NULL DEFAULT 0 CHECK (response_status BETWEEN 0 AND 599),
    result_public_id BLOB,
    response_body BLOB NOT NULL DEFAULT x'' CHECK (length(response_body) <= 8192),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(scope, key_hash)
) STRICT;

CREATE INDEX request_idempotencies_expiry_idx ON request_idempotencies(expires_at, id);

CREATE TABLE public_write_fingerprints (
    id INTEGER PRIMARY KEY,
    scope TEXT NOT NULL CHECK (length(scope) BETWEEN 1 AND 80),
    fingerprint_hash BLOB NOT NULL CHECK (length(fingerprint_hash) = 32),
    result_public_id BLOB,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    UNIQUE(scope, fingerprint_hash)
) STRICT;

CREATE INDEX public_write_fingerprints_expiry_idx ON public_write_fingerprints(expires_at, id);

-- Raw confirmation tokens are never stored.  token_ciphertext is encrypted
-- with the process secret so a durable worker can send a link after the
-- request has returned; token_hash remains the lookup/replay boundary.
CREATE TABLE newsletter_tokens (
    id INTEGER PRIMARY KEY,
    subscriber_id INTEGER NOT NULL REFERENCES newsletter_subscribers(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('confirm', 'unsubscribe')),
    token_hash BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    token_ciphertext BLOB NOT NULL CHECK (length(token_ciphertext) <= 256),
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER,
    created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX newsletter_tokens_lookup_idx ON newsletter_tokens(subscriber_id, purpose, expires_at);

-- A durable domain event is the source for the core event-dispatch job.  The
-- job carries only event_id; the bounded payload remains inspectable and
-- retryable here without coupling the generic jobs table to event fields.
CREATE TABLE event_outbox (
    id INTEGER PRIMARY KEY,
    event_id BLOB NOT NULL UNIQUE CHECK (length(event_id) = 16),
    event_name TEXT NOT NULL CHECK (length(event_name) BETWEEN 1 AND 120),
    event_version INTEGER NOT NULL CHECK (event_version > 0),
    object_public_id BLOB NOT NULL CHECK (length(object_public_id) <= 64),
    payload BLOB NOT NULL CHECK (length(payload) <= 16384),
    status TEXT NOT NULL CHECK (status IN ('pending', 'succeeded', 'failed')),
    occurred_at INTEGER NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX event_outbox_status_idx ON event_outbox(status, updated_at, id);

-- +goose Down
DROP INDEX event_outbox_status_idx;
DROP TABLE event_outbox;
DROP INDEX newsletter_tokens_lookup_idx;
DROP TABLE newsletter_tokens;
DROP INDEX public_write_fingerprints_expiry_idx;
DROP TABLE public_write_fingerprints;
DROP INDEX request_idempotencies_expiry_idx;
DROP TABLE request_idempotencies;

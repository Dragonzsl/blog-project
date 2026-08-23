-- +goose Up
CREATE TABLE system_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    render_epoch INTEGER NOT NULL DEFAULT 1 CHECK (render_epoch > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

INSERT INTO system_state (id, render_epoch, created_at, updated_at)
VALUES (1, 1, unixepoch('subsec') * 1000, unixepoch('subsec') * 1000);

CREATE TABLE jobs (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    payload_version INTEGER NOT NULL DEFAULT 1 CHECK (payload_version > 0),
    payload BLOB NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    available_at INTEGER NOT NULL,
    lease_expires_at INTEGER,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX jobs_runnable_idx ON jobs (status, available_at);

CREATE TABLE audit_entries (
    id INTEGER PRIMARY KEY,
    action TEXT NOT NULL,
    object_kind TEXT,
    object_public_id BLOB,
    result TEXT NOT NULL CHECK (result IN ('succeeded', 'failed')),
    request_id TEXT,
    context_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX audit_entries_created_idx ON audit_entries (created_at DESC);

CREATE TABLE backups (
    id INTEGER PRIMARY KEY,
    public_id BLOB NOT NULL UNIQUE CHECK (length(public_id) = 16),
    manifest_path TEXT NOT NULL,
    destination TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    checksum_status TEXT NOT NULL CHECK (checksum_status IN ('pending', 'valid', 'invalid')),
    encrypted INTEGER NOT NULL DEFAULT 0 CHECK (encrypted IN (0, 1)),
    restore_tested_at INTEGER,
    created_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE backups;
DROP INDEX audit_entries_created_idx;
DROP TABLE audit_entries;
DROP INDEX jobs_runnable_idx;
DROP TABLE jobs;
DROP TABLE system_state;

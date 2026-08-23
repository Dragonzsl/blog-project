-- +goose Up
CREATE TABLE sites (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    primary_language TEXT NOT NULL DEFAULT 'zh-CN',
    timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    base_url TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE owners (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL CHECK (length(username) BETWEEN 3 AND 64),
    username_key TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    totp_secret_cipher BLOB NOT NULL,
    auth_version INTEGER NOT NULL DEFAULT 1 CHECK (auth_version > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE owner_recovery_codes (
    id INTEGER PRIMARY KEY,
    owner_id INTEGER NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    code_hash BLOB NOT NULL UNIQUE CHECK (length(code_hash) = 32),
    created_at INTEGER NOT NULL,
    used_at INTEGER
) STRICT;

CREATE TABLE sessions (
    id INTEGER PRIMARY KEY,
    owner_id INTEGER NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    token_hash BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    auth_version INTEGER NOT NULL CHECK (auth_version > 0),
    expires_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX sessions_expiry_idx ON sessions (expires_at);

CREATE TABLE owner_setup_challenges (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token_hash BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    site_name TEXT NOT NULL,
    username TEXT NOT NULL,
    username_key TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    totp_secret_cipher BLOB NOT NULL,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE owner_setup_challenges;
DROP INDEX sessions_expiry_idx;
DROP TABLE sessions;
DROP TABLE owner_recovery_codes;
DROP TABLE owners;
DROP TABLE sites;

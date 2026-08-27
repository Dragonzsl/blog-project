-- +goose Up
CREATE TABLE themes (
    id INTEGER PRIMARY KEY,
    theme_id TEXT NOT NULL,
    version TEXT NOT NULL,
    name TEXT NOT NULL,
    api_version INTEGER NOT NULL CHECK (api_version > 0),
    core_range TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL,
    checksum BLOB NOT NULL CHECK (length(checksum) = 32),
    validation_status TEXT NOT NULL CHECK (validation_status IN ('valid', 'invalid')),
    validation_report TEXT NOT NULL DEFAULT '',
    active INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
    installed_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(theme_id, version)
) STRICT;

CREATE UNIQUE INDEX themes_active_idx ON themes(active) WHERE active = 1;
CREATE INDEX themes_catalog_idx ON themes(theme_id, version DESC, installed_at DESC);

CREATE TABLE theme_settings (
    theme_id INTEGER PRIMARY KEY REFERENCES themes(id) ON DELETE CASCADE,
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    values_json TEXT NOT NULL DEFAULT '{}',
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE plugin_states (
    plugin_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    version TEXT NOT NULL,
    api_version INTEGER NOT NULL CHECK (api_version > 0),
    kind TEXT NOT NULL DEFAULT 'general',
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    config_schema_version INTEGER NOT NULL DEFAULT 1 CHECK (config_schema_version > 0),
    last_init_result TEXT NOT NULL DEFAULT 'disabled',
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX plugin_states_enabled_idx ON plugin_states(enabled, kind, plugin_id);

CREATE TABLE plugin_settings (
    plugin_id TEXT PRIMARY KEY REFERENCES plugin_states(plugin_id) ON DELETE CASCADE,
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    values_json TEXT NOT NULL DEFAULT '{}',
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE comments (
    id INTEGER PRIMARY KEY,
    public_id BLOB NOT NULL UNIQUE CHECK (length(public_id) = 16),
    content_id INTEGER NOT NULL REFERENCES contents(id) ON DELETE CASCADE,
    parent_id INTEGER REFERENCES comments(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 120),
    email_ciphertext BLOB NOT NULL DEFAULT x'',
    email_hash BLOB NOT NULL DEFAULT x'',
    website TEXT NOT NULL DEFAULT '',
    body_markdown TEXT NOT NULL CHECK (length(body_markdown) BETWEEN 1 AND 10000),
    body_html TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'spam', 'trash')),
    created_at INTEGER NOT NULL,
    approved_at INTEGER,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX comments_content_status_idx ON comments(content_id, status, created_at, id);
CREATE INDEX comments_parent_idx ON comments(parent_id, created_at, id);

CREATE TABLE analytics_daily (
    day TEXT NOT NULL,
    path TEXT NOT NULL,
    views INTEGER NOT NULL DEFAULT 0 CHECK (views >= 0),
    unique_visitors INTEGER NOT NULL DEFAULT 0 CHECK (unique_visitors >= 0),
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(day, path)
) STRICT;

CREATE INDEX analytics_daily_day_idx ON analytics_daily(day DESC, path);

CREATE TABLE analytics_visitor_days (
    day TEXT NOT NULL,
    visitor_hash BLOB NOT NULL CHECK (length(visitor_hash) = 32),
    PRIMARY KEY(day, visitor_hash)
) STRICT;

CREATE TABLE notification_outbox (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    recipient TEXT NOT NULL,
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at INTEGER NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    sent_at INTEGER
) STRICT;

CREATE INDEX notification_outbox_runnable_idx ON notification_outbox(status, available_at, id);

CREATE TABLE newsletter_subscribers (
    id INTEGER PRIMARY KEY,
    email_hash BLOB NOT NULL UNIQUE CHECK (length(email_hash) = 32),
    email_ciphertext BLOB NOT NULL,
    provider TEXT NOT NULL DEFAULT 'local',
    status TEXT NOT NULL CHECK (status IN ('pending', 'active', 'unsubscribed')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE media_storage_locations (
    media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    variant_key TEXT NOT NULL,
    adapter TEXT NOT NULL,
    object_key TEXT NOT NULL,
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    content_hash BLOB NOT NULL CHECK (length(content_hash) = 32),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(media_id, variant_key),
    UNIQUE(adapter, object_key)
) STRICT;

CREATE INDEX media_storage_adapter_idx ON media_storage_locations(adapter, media_id);

INSERT INTO media_storage_locations(media_id, variant_key, adapter, object_key, size_bytes, content_hash, created_at, updated_at)
SELECT id, 'original', 'local', object_key, size_bytes, content_hash, created_at, updated_at FROM media;
INSERT INTO media_storage_locations(media_id, variant_key, adapter, object_key, size_bytes, content_hash, created_at, updated_at)
SELECT media_id, variant_key, 'local', object_key, size_bytes, content_hash, created_at, created_at FROM media_variants;

CREATE TABLE storage_migrations (
    id INTEGER PRIMARY KEY,
    source_adapter TEXT NOT NULL,
    destination_adapter TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    total_objects INTEGER NOT NULL DEFAULT 0 CHECK (total_objects >= 0),
    copied_objects INTEGER NOT NULL DEFAULT 0 CHECK (copied_objects >= 0),
    failed_objects INTEGER NOT NULL DEFAULT 0 CHECK (failed_objects >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL,
    completed_at INTEGER
) STRICT;

-- +goose Down
DROP TABLE storage_migrations;
DROP INDEX media_storage_adapter_idx;
DROP TABLE media_storage_locations;
DROP INDEX notification_outbox_runnable_idx;
DROP TABLE newsletter_subscribers;
DROP TABLE notification_outbox;
DROP INDEX analytics_daily_day_idx;
DROP TABLE analytics_visitor_days;
DROP TABLE analytics_daily;
DROP INDEX comments_parent_idx;
DROP INDEX comments_content_status_idx;
DROP TABLE comments;
DROP TABLE plugin_settings;
DROP INDEX plugin_states_enabled_idx;
DROP TABLE plugin_states;
DROP TABLE theme_settings;
DROP INDEX themes_catalog_idx;
DROP INDEX themes_active_idx;
DROP TABLE themes;

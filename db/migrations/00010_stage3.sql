-- +goose Up

-- A source fingerprint makes offline imports repeatable without putting
-- importer-specific columns on the publishing tables.  The report is kept so
-- a dry-run can be inspected after a real import without retaining the source
-- file itself.
CREATE TABLE import_runs (
    id INTEGER PRIMARY KEY,
    source_format TEXT NOT NULL,
    source_fingerprint TEXT NOT NULL CHECK (length(source_fingerprint) = 64),
    source_name TEXT NOT NULL DEFAULT '',
    report_json TEXT NOT NULL DEFAULT '{}',
    imported_count INTEGER NOT NULL DEFAULT 0 CHECK (imported_count >= 0),
    created_at INTEGER NOT NULL,
    UNIQUE (source_format, source_fingerprint)
) STRICT;

CREATE INDEX import_runs_created_idx ON import_runs(created_at DESC);

-- Webhook deliveries are intentionally separate from the generic job payload:
-- operators can inspect a delivery and its last error without decoding a
-- plugin-owned payload.  The job table remains the durable scheduler and
-- applies the retry/lease policy shared by all extensions.
CREATE TABLE webhook_deliveries (
    id INTEGER PRIMARY KEY,
    delivery_id TEXT NOT NULL UNIQUE,
    event_name TEXT NOT NULL,
    event_version INTEGER NOT NULL CHECK (event_version > 0),
    endpoint TEXT NOT NULL,
    payload BLOB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT,
    delivered_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX webhook_deliveries_status_idx ON webhook_deliveries(status, updated_at DESC);

-- +goose Down
DROP INDEX webhook_deliveries_status_idx;
DROP TABLE webhook_deliveries;
DROP INDEX import_runs_created_idx;
DROP TABLE import_runs;

-- Session-level token usage aggregates (one row per session+model; model uses
-- the '(unknown)'/'(mixed)' sentinels when the source cannot attribute) and
-- last-observed quota snapshots. No monetary amounts are ever stored.
CREATE TABLE IF NOT EXISTS session_usage (
    session_uuid          TEXT NOT NULL,
    source                TEXT NOT NULL,
    model                 TEXT NOT NULL,
    input_tokens          INTEGER NOT NULL DEFAULT 0,
    output_tokens         INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens      INTEGER NOT NULL DEFAULT 0,
    tool_tokens           INTEGER NOT NULL DEFAULT 0,
    total_tokens          INTEGER NOT NULL DEFAULT 0,
    categories_json       TEXT,
    PRIMARY KEY (session_uuid, model)
);

CREATE INDEX IF NOT EXISTS idx_session_usage_total ON session_usage(total_tokens);

-- Last-observed quota snapshot per (source, limit id). observed_at is mandatory:
-- surfaces must render age/staleness, never present a snapshot as live.
CREATE TABLE IF NOT EXISTS quota_snapshots (
    source         TEXT NOT NULL,
    limit_id       TEXT NOT NULL,
    observed_at    INTEGER NOT NULL,
    used_percent   REAL,
    window_minutes INTEGER,
    resets_at      INTEGER,
    plan_type      TEXT,
    raw_json       TEXT,
    PRIMARY KEY (source, limit_id)
);

-- Per-file usage diagnostics: skipped (should-carry-usage but malformed),
-- unmapped (category names preserved in categories_json), stale (last usage
-- scan failed mid-pass; aggregate retained but no longer known-current).
ALTER TABLE ingest_state ADD COLUMN usage_skipped INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ingest_state ADD COLUMN usage_unmapped INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ingest_state ADD COLUMN usage_stale INTEGER NOT NULL DEFAULT 0;

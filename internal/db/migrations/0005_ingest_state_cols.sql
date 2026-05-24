-- head_fingerprint: hash of the file's leading bytes, validated alongside the tail
-- fingerprint to catch a pre-watermark rewrite. Empty means "unknown" (a row written
-- before this migration): the resume path skips the head check and backfills it, so
-- existing installs are not forced into a full reingest on upgrade.
ALTER TABLE ingest_state ADD COLUMN head_fingerprint TEXT NOT NULL DEFAULT '';

-- unparsed_lines: running count of complete lines that could not be parsed for this
-- source. Accumulates across incremental passes; reset on full reingest. Surfaced by
-- `clio doctor`.
ALTER TABLE ingest_state ADD COLUMN unparsed_lines INTEGER NOT NULL DEFAULT 0;

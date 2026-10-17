-- source_missing_at: unix seconds when reconciliation first found this source file
-- gone (NULL = present). Claude Code deletes transcripts after cleanupPeriodDays
-- (default 30); clio keeps the indexed rows and only marks them, so its history
-- outlives that window. Deletion is the explicit `clio index --purge-missing`.
ALTER TABLE ingest_state ADD COLUMN source_missing_at INTEGER;

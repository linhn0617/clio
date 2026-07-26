-- Narrow the messages UPDATE trigger to content-only, so a raw_json-only UPDATE
-- (clio prune-raw) does NOT fire an FTS delete+reinsert. Normal ingest never
-- UPDATEs content (INSERT OR IGNORE for new rows, DELETE+INSERT on full rebuild),
-- so scoping to `OF content` is behavior-preserving for every existing path.
DROP TRIGGER IF EXISTS messages_au;
CREATE TRIGGER messages_au AFTER UPDATE OF content ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
    INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;

-- Explicit successful-vs-aborted ingest signal for prune-raw's restorability
-- predicate. Fail-closed on upgrade: every existing row is marked unverified (1)
-- until a successful commit (a full reindex) clears it, so prune-raw never
-- treats a pre-migration snapshot as restorable on trust.
ALTER TABLE ingest_state ADD COLUMN aborted INTEGER NOT NULL DEFAULT 0;
UPDATE ingest_state SET aborted = 1;

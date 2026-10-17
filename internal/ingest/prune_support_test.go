package ingest

import (
	"context"
	"testing"
)

// Migration 0012: the messages_au trigger is scoped to content, so a
// raw_json-only UPDATE leaves the FTS index untouched (integrity-check passes),
// while a content UPDATE still re-indexes.
func TestMigration0012TriggerScopedToContent(t *testing.T) {
	projects := t.TempDir()
	_ = writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := database.QueryRow(`SELECT id FROM messages LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// Snapshot FTS row count, then a raw_json-only UPDATE, then integrity-check.
	var beforeFTS int64
	database.QueryRow(`SELECT count(*) FROM messages_fts`).Scan(&beforeFTS)
	if _, err := database.Exec(`UPDATE messages SET raw_json='' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	var afterFTS int64
	database.QueryRow(`SELECT count(*) FROM messages_fts`).Scan(&afterFTS)
	if beforeFTS != afterFTS {
		t.Fatalf("raw_json-only UPDATE changed FTS row count %d -> %d", beforeFTS, afterFTS)
	}
	if _, err := database.Exec(`INSERT INTO messages_fts(messages_fts) VALUES('integrity-check')`); err != nil {
		t.Fatalf("FTS integrity-check failed after raw_json-only update: %v", err)
	}
	// A content UPDATE still re-indexes: searching the new content finds it.
	if _, err := database.Exec(`UPDATE messages SET content='zzzuniquetoken' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	var hit int64
	database.QueryRow(`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'zzzuniquetoken'`).Scan(&hit)
	if hit == 0 {
		t.Fatal("content UPDATE did not re-index (FTS should still fire on content changes)")
	}
}

// The aborted flag: migration marks pre-existing rows 1 (fail-closed); a
// successful full ingest clears it to 0.
func TestAbortedFlagLifecycle(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	// A successful ingest leaves aborted=0.
	var aborted int64
	if err := database.QueryRow(`SELECT aborted FROM ingest_state WHERE source_file=?`, path).Scan(&aborted); err != nil {
		t.Fatal(err)
	}
	if aborted != 0 {
		t.Fatalf("successful ingest aborted=%d want 0", aborted)
	}
	// Simulate a pre-migration/unverified row, then a full re-ingest clears it.
	if _, err := database.Exec(`UPDATE ingest_state SET aborted=1 WHERE source_file=?`, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT aborted FROM ingest_state WHERE source_file=?`, path).Scan(&aborted)
	if aborted != 0 {
		t.Fatalf("full re-ingest did not clear aborted (=%d)", aborted)
	}
}

// The migration's fail-closed init statement (`UPDATE ingest_state SET
// aborted=1`) must flip a pre-existing successful row to 1. We can't replay a
// pre-0012 DB here, but we can assert the exact statement's semantics on a
// seeded row.
func TestMigrationAbortedInitStatementSemantics(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.Exec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, last_ingested_at, aborted) VALUES ('pre.jsonl',10,10,10,'',1,0)`); err != nil {
		t.Fatal(err)
	}
	// This is the literal statement migration 0012 runs for existing rows.
	if _, err := database.Exec(`UPDATE ingest_state SET aborted = 1`); err != nil {
		t.Fatal(err)
	}
	var aborted int64
	database.QueryRow(`SELECT aborted FROM ingest_state WHERE source_file='pre.jsonl'`).Scan(&aborted)
	if aborted != 1 {
		t.Fatalf("fail-closed init left aborted=%d want 1", aborted)
	}
}

// A genuine pre-0012 upgrade: drop the aborted column, seed a row (as if
// pre-migration), then run migration 0012's exact statements and assert the
// existing row is upgraded to aborted=1 (fail-closed).
func TestMigration0012AbortedDDLUpgradesExistingRow(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.Exec(`ALTER TABLE ingest_state DROP COLUMN aborted`); err != nil {
		t.Skipf("DROP COLUMN unsupported on this SQLite build: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, last_ingested_at) VALUES ('pre0012.jsonl',5,5,5,'',1)`); err != nil {
		t.Fatal(err)
	}
	// Migration 0012's fail-closed column init (verbatim).
	if _, err := database.Exec(`ALTER TABLE ingest_state ADD COLUMN aborted INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE ingest_state SET aborted = 1`); err != nil {
		t.Fatal(err)
	}
	var aborted int64
	database.QueryRow(`SELECT aborted FROM ingest_state WHERE source_file='pre0012.jsonl'`).Scan(&aborted)
	if aborted != 1 {
		t.Fatalf("pre-0012 row upgraded to aborted=%d want 1 (fail-closed)", aborted)
	}
}

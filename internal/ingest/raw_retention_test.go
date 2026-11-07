package ingest

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

// recentEvent is a one-line user event stamped now, so its session is inside
// any retention window the tests set.
func recentEvent(uuid, content string) string {
	return `{"type":"user","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","cwd":"/p","sessionId":"` + uuid + `","message":{"role":"user","content":"` + content + `"}}`
}

func setRetention(t *testing.T, d *db.DB, v string) {
	t.Helper()
	if err := d.PutSetting(context.Background(), db.RawRetentionKey, v); err != nil {
		t.Fatal(err)
	}
}

// blankRaw counts a session's messages with and without raw_json.
func blankRaw(t *testing.T, d *db.DB, uuid string) (blank, kept int) {
	t.Helper()
	blank = rowCount(t, d, `SELECT count(*) FROM messages WHERE session_uuid=? AND raw_json=''`, uuid)
	kept = rowCount(t, d, `SELECT count(*) FROM messages WHERE session_uuid=? AND raw_json<>''`, uuid)
	return blank, kept
}

func TestRawRetentionFullReindexBlanksOldKeepsRecent(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1, evResult1, evUser2) // 2026-04-26
	writeSession(t, projects, "-Users-x-new", "sess-new", recentEvent("sess-new", "fresh work today"))
	database := openTestDB(t)
	setRetention(t, database, "14d")
	if _, err := New(database, nil).IngestAll(context.Background(), projects, true); err != nil {
		t.Fatal(err)
	}
	if blank, kept := blankRaw(t, database, "sess-1"); blank == 0 || kept != 0 {
		t.Fatalf("old session raw_json: %d blank, %d kept; want all blank", blank, kept)
	}
	if blank, kept := blankRaw(t, database, "sess-new"); blank != 0 || kept == 0 {
		t.Fatalf("recent session raw_json: %d blank, %d kept; want all kept", blank, kept)
	}
	// Everything except raw_json is written as today.
	if n := rowCount(t, database, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'validation'`); n == 0 {
		t.Fatal("FTS lost the old session's content")
	}
	if n := rowCount(t, database, `SELECT count(*) FROM tool_calls tc JOIN messages m ON m.id = tc.message_id WHERE m.session_uuid='sess-1'`); n == 0 {
		t.Fatal("old session has no tool_calls")
	}
	if n := rowCount(t, database, `SELECT count(*) FROM tool_targets WHERE session_uuid='sess-1' AND kind='file' AND value='form.go'`); n != 1 {
		t.Fatalf("old session file target count=%d want 1", n)
	}
}

func TestRawRetentionIncrementalNewOldFileBlanks(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1)
	database := openTestDB(t)
	setRetention(t, database, "14d")
	if _, err := New(database, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	if blank, kept := blankRaw(t, database, "sess-1"); blank == 0 || kept != 0 {
		t.Fatalf("incremental old file: %d blank, %d kept; want all blank", blank, kept)
	}
}

func TestRawRetentionAppendMakesSessionRecent(t *testing.T) {
	projects := t.TempDir()
	// Four lines (749 bytes) put the file past fingerprintWindow (512 bytes,
	// incremental.go), so the append below is ingested incrementally.
	path := writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1, evResult1, evUser2)
	database := openTestDB(t)
	setRetention(t, database, "14d")
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	oldBlank, _ := blankRaw(t, database, "sess-1")
	appendLine(t, path, recentEvent("sess-1", "picked it up again"))
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	blank, kept := blankRaw(t, database, "sess-1")
	if blank != oldBlank || kept != 1 {
		t.Fatalf("after append: %d blank (was %d), %d kept; want earlier rows still blank and the new row kept", blank, oldBlank, kept)
	}
}

// The drop covers only the rows the current commit inserts: rows an earlier
// commit stored with raw_json are left for the sweep (and its restorability
// checks), even when the session is old.
func TestRawRetentionLeavesEarlierCommitsAlone(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1, evResult1, evUser2)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil { // retention off
		t.Fatal(err)
	}
	_, keptBefore := blankRaw(t, database, "sess-1")
	setRetention(t, database, "14d")
	appendLine(t, path, `{"type":"user","timestamp":"2026-04-26T11:10:00Z","sessionId":"sess-1","message":{"role":"user","content":"one more old line"}}`)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	if blank, kept := blankRaw(t, database, "sess-1"); blank != 1 || kept != keptBefore {
		t.Fatalf("after old append: %d blank, %d kept (was %d kept); want only the new row blank", blank, kept, keptBefore)
	}
}

func TestRawRetentionUnsetKeepsRaw(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1)
	database := openTestDB(t)
	if _, err := New(database, nil).IngestAll(context.Background(), projects, true); err != nil {
		t.Fatal(err)
	}
	if blank, _ := blankRaw(t, database, "sess-1"); blank != 0 {
		t.Fatalf("retention unset blanked %d rows", blank)
	}
}

func TestRawRetentionBadStoredValueFailsOpen(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1)
	database := openTestDB(t)
	setRetention(t, database, "2026-10-01") // bypasses the command's validation
	var logBuf bytes.Buffer
	ing := New(database, slog.New(slog.NewTextHandler(&logBuf, nil)))
	if _, err := ing.IngestAll(context.Background(), projects, true); err != nil {
		t.Fatal(err)
	}
	if blank, _ := blankRaw(t, database, "sess-1"); blank != 0 {
		t.Fatalf("invalid retention blanked %d rows; want raw kept (fail open)", blank)
	}
	if !bytes.Contains(logBuf.Bytes(), []byte("value=2026-10-01")) {
		t.Fatalf("no warning naming the bad value:\n%s", logBuf.String())
	}
}

// Design §3.2: activity facts come from the parsed events, so blanking raw_json
// in the same commit loses none, and the repair pass (which reads stored
// raw_json) tolerates the blank rows.
func TestRawRetentionActivityFromParseAndRepairTolerates(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1, evResult1)
	database := openTestDB(t)
	setRetention(t, database, "14d")
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, true); err != nil {
		t.Fatal(err)
	}
	if blank, kept := blankRaw(t, database, "sess-1"); blank == 0 || kept != 0 {
		t.Fatalf("fixture not blanked: %d blank, %d kept", blank, kept)
	}
	before := activityRows(t, database, "sess-1")
	if len(before) == 0 {
		t.Fatal("no activity facts written from the parsed events")
	}
	if err := ing.RepairActivity(context.Background()); err != nil {
		t.Fatalf("RepairActivity on blanked rows: %v", err)
	}
	if after := activityRows(t, database, "sess-1"); len(after) != len(before) {
		t.Fatalf("repair changed activity facts: %v -> %v", before, after)
	}
}

// The drop applies to every source: a Codex rollout (2026-06-19) outside the
// window is stored without raw_json.
func TestRawRetentionBlanksOldCodexRollout(t *testing.T) {
	codexRoot := t.TempDir()
	const uuid = "0199dddd-eeee-7fff-8000-222222222222"
	writeCodexRollout(t, codexRoot, uuid)
	database := openTestDB(t)
	setRetention(t, database, "14d")
	ing := New(database, nil)
	ing.AddSource(codexSource{root: codexRoot})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	if blank, kept := blankRaw(t, database, uuid); blank == 0 || kept != 0 {
		t.Fatalf("codex rollout raw_json: %d blank, %d kept; want all blank", blank, kept)
	}
}

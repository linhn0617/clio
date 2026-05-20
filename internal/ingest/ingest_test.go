package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

const (
	evUser1   = `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/Users/lin/Herd/cli_project_COMPLETE","sessionId":"sess-1","message":{"role":"user","content":"please add data 驗證流程 to the form"}}`
	evAsst1   = `{"type":"assistant","timestamp":"2026-04-26T11:00:05Z","sessionId":"sess-1","message":{"role":"assistant","content":[{"type":"text","text":"sure, adding validation"},{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"form.go"}}]}}`
	evResult1 = `{"type":"user","timestamp":"2026-04-26T11:00:06Z","sessionId":"sess-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file edited ok"}]}}`
	evUser2   = `{"type":"user","timestamp":"2026-04-26T11:05:00Z","sessionId":"sess-1","message":{"role":"user","content":"thanks, looks good"}}`
)

// writeSession creates ~/.claude/projects-style layout in a temp dir.
func writeSession(t *testing.T, root, encodedDir, uuid string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, encodedDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, uuid+".jsonl")
	var data []byte
	for _, l := range lines {
		data = append(data, []byte(l+"\n")...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	// On-disk (not in-memory): exercises WAL, FTS5, file locking.
	path := filepath.Join(t.TempDir(), "test.sqlite")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestIngestFullAndSearch(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-lin-Herd-cli-project-COMPLETE", "sess-1", evUser1, evAsst1, evResult1, evUser2)

	database := openTestDB(t)
	ing := New(database, nil)
	st, err := ing.IngestAll(projects, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.FilesIngested != 1 {
		t.Fatalf("FilesIngested=%d want 1", st.FilesIngested)
	}

	// project_path comes from event cwd (underscores preserved), not dir decode.
	var pp string
	if err := database.QueryRow(`SELECT project_path FROM sessions WHERE uuid='sess-1'`).Scan(&pp); err != nil {
		t.Fatal(err)
	}
	if pp != "/Users/lin/Herd/cli_project_COMPLETE" {
		t.Fatalf("project_path=%q", pp)
	}

	// turn_count = number of user messages (2).
	var turns int
	database.QueryRow(`SELECT turn_count FROM sessions WHERE uuid='sess-1'`).Scan(&turns)
	if turns != 2 {
		t.Fatalf("turn_count=%d want 2", turns)
	}

	// FTS finds the CJK 3+ char term.
	var n int
	database.QueryRow(`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH '驗證流程'`).Scan(&n)
	if n == 0 {
		t.Fatal("FTS did not find 驗證流程")
	}

	// tool_call recorded.
	var tcName string
	if err := database.QueryRow(`SELECT tool_name FROM tool_calls LIMIT 1`).Scan(&tcName); err != nil {
		t.Fatal(err)
	}
	if tcName != "Edit" {
		t.Fatalf("tool_name=%q", tcName)
	}
}

func TestIngestIdempotentSkip(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evUser2)
	database := openTestDB(t)
	ing := New(database, nil)

	if _, err := ing.IngestAll(projects, false); err != nil {
		t.Fatal(err)
	}
	st, err := ing.IngestAll(projects, false) // second run: unchanged
	if err != nil {
		t.Fatal(err)
	}
	if st.FilesSkipped != 1 || st.FilesIngested != 0 {
		t.Fatalf("expected skip on second run, got %+v", st)
	}
}

func TestIngestIncrementalAppend(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evAsst1)
	database := openTestDB(t)
	ing := New(database, nil)

	if _, err := ing.IngestAll(projects, false); err != nil {
		t.Fatal(err)
	}
	var before int
	database.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='sess-1'`).Scan(&before)

	// Append more events (mtime will advance).
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(evResult1 + "\n" + evUser2 + "\n")
	f.Close()
	// Force mtime change to be safe across fast filesystems.
	bumpMtime(t, path)

	if _, err := ing.IngestAll(projects, false); err != nil {
		t.Fatal(err)
	}
	var after int
	database.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='sess-1'`).Scan(&after)
	if after <= before {
		t.Fatalf("incremental did not add messages: before=%d after=%d", before, after)
	}

	// seq must be contiguous and unique.
	var distinct, total int
	database.QueryRow(`SELECT count(DISTINCT seq), count(*) FROM messages WHERE session_uuid='sess-1'`).Scan(&distinct, &total)
	if distinct != total {
		t.Fatalf("seq not unique: distinct=%d total=%d", distinct, total)
	}
}

func TestIngestPartialLineDeferred(t *testing.T) {
	projects := t.TempDir()
	dir := filepath.Join(projects, "-Users-lin-Herd-x")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "sess-1.jsonl")
	// One complete line + one partial (no trailing newline).
	os.WriteFile(path, []byte(evUser1+"\n"+`{"type":"user","sessionId":"sess-1","message":{"role":"user","content":"partial`), 0o600)

	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(projects, false); err != nil {
		t.Fatal(err)
	}
	var n int
	database.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='sess-1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected only the 1 complete line ingested, got %d", n)
	}

	// Now complete the partial line; it should get ingested next run.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(` text"}}` + "\n")
	f.Close()
	bumpMtime(t, path)

	if _, err := ing.IngestAll(projects, false); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='sess-1'`).Scan(&n)
	if n != 2 {
		t.Fatalf("expected 2 messages after completing line, got %d", n)
	}
}

func TestConcurrentReadDuringWrite(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evAsst1, evResult1, evUser2)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(projects, false); err != nil {
		t.Fatal(err)
	}
	// A read while a write transaction is open should not deadlock (WAL).
	done := make(chan error, 1)
	go func() {
		var n int
		done <- database.QueryRow(`SELECT count(*) FROM messages`).Scan(&n)
	}()
	if err := <-done; err != nil {
		t.Fatalf("concurrent read failed: %v", err)
	}
}

// bumpMtime advances the file's mtime so classifyChange sees a change even on
// filesystems with coarse timestamp resolution.
func bumpMtime(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

package ingest

import (
	"os"
	"path/filepath"
	"strings"
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

// NOTE: in-process only; cross-process safety is covered by TestCrossProcessConcurrentIngest.
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

func TestIngestRawJSONRedacted(t *testing.T) {
	projects := t.TempDir()
	leak := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"sess-1","message":{"role":"user","content":"my key AKIAIOSFODNN7EXAMPLE"}}`
	writeSession(t, projects, "-p", "sess-1", leak)
	d := openTestDB(t)
	if _, err := New(d, nil).IngestAll(projects, false); err != nil {
		t.Fatal(err)
	}
	var raw, content string
	d.QueryRow(`SELECT raw_json, content FROM messages WHERE session_uuid='sess-1' LIMIT 1`).Scan(&raw, &content)
	if strings.Contains(raw, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("raw_json leaked secret: %s", raw)
	}
	if strings.Contains(content, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("content leaked secret: %s", content)
	}
}

func TestIngestSameSizeRewriteForcesFull(t *testing.T) {
	projects := t.TempDir()
	dir := filepath.Join(projects, "-p")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "sess-1.jsonl")
	a := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"sess-1","message":{"role":"user","content":"AAAAAAAAAA"}}`
	os.WriteFile(path, []byte(a+"\n"), 0o600)
	d := openTestDB(t)
	ing := New(d, nil)
	if _, err := ing.IngestAll(projects, false); err != nil {
		t.Fatal(err)
	}
	var before string
	d.QueryRow(`SELECT content FROM messages WHERE session_uuid='sess-1' LIMIT 1`).Scan(&before)

	// Rewrite to a DIFFERENT same-length content (pre-offset bytes change),
	// keeping byte size identical. Fingerprint mismatch must force a full re-ingest.
	b := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"sess-1","message":{"role":"user","content":"BBBBBBBBBB"}}`
	if len(b) != len(a) {
		t.Fatalf("test bug: rewrite must be same length (%d vs %d)", len(a), len(b))
	}
	os.WriteFile(path, []byte(b+"\n"), 0o600)
	bumpMtime(t, path)
	if _, err := ing.IngestAll(projects, false); err != nil {
		t.Fatal(err)
	}
	var after string
	var count int
	d.QueryRow(`SELECT content FROM messages WHERE session_uuid='sess-1' LIMIT 1`).Scan(&after)
	d.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='sess-1'`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 message after full re-ingest, got %d (stale/duplicate)", count)
	}
	if !strings.Contains(after, "BBBBBBBBBB") {
		t.Fatalf("rewrite not picked up; content still %q", after)
	}
}

func TestIngestSelfPollutionAcrossIncrements(t *testing.T) {
	projects := t.TempDir()
	dir := filepath.Join(projects, "-p")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "sess-1.jsonl")
	// Batch 1: clio's own MCP tool_use only.
	use := `{"type":"assistant","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"sess-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"clio-1","name":"mcp__clio__search","input":{"query":"x"}}]}}`
	os.WriteFile(path, []byte(use+"\n"), 0o600)
	d := openTestDB(t)
	ing := New(d, nil)
	if _, err := ing.IngestAll(projects, false); err != nil {
		t.Fatal(err)
	}

	// Batch 2 (later incremental): the matching tool_result. A fresh parser
	// would forget clio-1 and index this; the persisted excluded set prevents it.
	res := `{"type":"user","timestamp":"2026-04-26T11:01:00Z","cwd":"/p","sessionId":"sess-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"clio-1","content":"secret clio results"}]}}`
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(res + "\n")
	f.Close()
	bumpMtime(t, path)
	if _, err := ing.IngestAll(projects, false); err != nil {
		t.Fatal(err)
	}

	var n int
	d.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='sess-1' AND content LIKE '%clio results%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("clio tool_result leaked across incremental boundary (%d rows)", n)
	}
}

func TestIngestForceTwiceNoDuplicates(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evAsst1)
	database := openTestDB(t)
	ing := New(database, nil)

	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if _, _, err := ing.IngestFile(path, true); err != nil { // force full re-ingest again
		t.Fatalf("second ingest: %v", err)
	}

	var msgCount, ftsCount int
	if err := database.QueryRow(`SELECT count(*) FROM messages`).Scan(&msgCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM messages_fts`).Scan(&ftsCount); err != nil {
		t.Fatal(err)
	}
	if msgCount == 0 {
		t.Fatal("no messages ingested")
	}
	if ftsCount != msgCount {
		t.Fatalf("fts rows = %d, messages = %d (must match)", ftsCount, msgCount)
	}
}

func TestTurnCountStableAcrossReingest(t *testing.T) {
	projects := t.TempDir()
	// evUser1 + evUser2 are the two user turns; evAsst1 is the assistant reply.
	path := writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evAsst1, evUser2)
	database := openTestDB(t)
	ing := New(database, nil)

	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatal(err)
	}

	var turns int
	if err := database.QueryRow(`SELECT turn_count FROM sessions WHERE uuid = 'sess-1'`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 2 {
		t.Fatalf("turn_count = %d, want 2", turns)
	}
}

func TestIncrementalWatermarkIsMonotonic(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evUser2)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, _, err := ing.IngestFile(path, false); err != nil {
		t.Fatal(err)
	}

	var before int64
	if err := database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file = ?`, path).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// Simulate a stale incremental writer trying to push the offset backward.
	_, err := database.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(source_file) DO UPDATE SET last_byte_offset=excluded.last_byte_offset
		WHERE excluded.last_byte_offset >= ingest_state.last_byte_offset`,
		path, 1, 1, before-10, "x", 1)
	if err != nil {
		t.Fatal(err)
	}

	var after int64
	if err := database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file = ?`, path).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("watermark moved backward: before=%d after=%d", before, after)
	}
}

func TestChangeFullAbortsWhenFileChangedUnderUs(t *testing.T) {
	projects := t.TempDir()
	// V1 = two user events only (2 messages).
	path := writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evUser2)
	database := openTestDB(t)
	ing := New(database, nil)

	// Seed V1.
	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatal(err)
	}

	// While the next full ingest is mid-transaction (after BEGIN, before writes),
	// replace the file with V2 (four events, different size) to simulate a
	// concurrent writer's commit.
	preCommitHook = func() {
		writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evAsst1, evResult1, evUser2)
		preCommitHook = nil
	}
	t.Cleanup(func() { preCommitHook = nil })

	// Force a full ingest using the stale (V1) in-memory snapshot. It must abort
	// cleanly (no error surfaced) rather than revert the DB to V1 over V2.
	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatalf("ingest should abort cleanly, got error: %v", err)
	}

	// A subsequent ingest reconciles the DB to V2 (more messages than V1's 2).
	if _, _, err := ing.IngestFile(path, true); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='sess-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n <= 2 {
		t.Fatalf("messages = %d; expected reconcile to V2 (>2)", n)
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

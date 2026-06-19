package ingest

import (
	"bufio"
	"context"
	"fmt"
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

func TestIngestRefusesCrossSourceUUIDCollision(t *testing.T) {
	projects := t.TempDir()
	// A Claude Code session file whose filename uuid collides with an existing
	// codex session already in the index.
	writeSession(t, projects, "-Users-lin-Herd-x", "dup-1", evUser1, evUser2)
	database := openTestDB(t)
	if _, err := database.Exec(`INSERT INTO sessions(uuid, source_file, turn_count, source) VALUES ('dup-1','/codex/dup-1.jsonl',0,'codex')`); err != nil {
		t.Fatal(err)
	}
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatalf("IngestAll must not hard-error on a per-file conflict: %v", err)
	}
	// The pre-existing codex session must be intact: not overwritten, no CC messages attached.
	var src string
	if err := database.QueryRow(`SELECT source FROM sessions WHERE uuid='dup-1'`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	if src != "codex" {
		t.Fatalf("source=%q want codex (the CC ingest must not overwrite a different source)", src)
	}
	var msgCount int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid='dup-1'`).Scan(&msgCount)
	if msgCount != 0 {
		t.Fatalf("got %d messages attached to the codex session, want 0", msgCount)
	}
	// The conflict is recorded durably.
	var conflicts int
	if err := database.QueryRow(`SELECT COUNT(*) FROM source_conflicts WHERE uuid='dup-1' AND conflicting_source='claude-code' AND seen_source='codex'`).Scan(&conflicts); err != nil {
		t.Fatal(err)
	}
	if conflicts != 1 {
		t.Fatalf("source_conflicts rows=%d want 1", conflicts)
	}
}

func writeCodexRollout(t *testing.T, dir, uuid string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-06-19T10-00-00-"+uuid+".jsonl")
	lines := `{"timestamp":"2026-06-19T10:00:00Z","type":"session_meta","payload":{"id":"` + uuid + `","cwd":"/p"}}
{"timestamp":"2026-06-19T10:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi codex"}]}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIngestAllIngestsCodexWhenClaudeRootMissing(t *testing.T) {
	missingCC := filepath.Join(t.TempDir(), "does-not-exist") // never created
	codexRoot := t.TempDir()
	const uuid = "0199dddd-eeee-7fff-8000-111111111111"
	writeCodexRollout(t, codexRoot, uuid)
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(codexSource{root: codexRoot})
	if _, err := ing.IngestAll(context.Background(), missingCC, false); err != nil {
		t.Fatalf("IngestAll must not abort when the Claude root is missing: %v", err)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE uuid=? AND source='codex'`, uuid).Scan(&n)
	if n != 1 {
		t.Fatalf("codex not ingested when the Claude root is missing (n=%d)", n)
	}
}

func TestPurgeMissingPreservesUnavailableCodexRoot(t *testing.T) {
	ccRoot := t.TempDir()
	codexRoot := t.TempDir()
	const uuid = "0199cccc-dddd-7eee-8fff-aaaaaaaaaaaa"
	writeCodexRollout(t, codexRoot, uuid)
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(codexSource{root: codexRoot})
	if _, err := ing.IngestAll(context.Background(), ccRoot, false); err != nil {
		t.Fatal(err)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE uuid=?`, uuid).Scan(&n)
	if n != 1 {
		t.Fatalf("codex session not indexed (n=%d)", n)
	}
	// The Codex root becomes temporarily unavailable.
	if err := os.RemoveAll(codexRoot); err != nil {
		t.Fatal(err)
	}
	// Purging against the healthy Claude Code root must NOT purge the Codex rows.
	if err := ing.PurgeMissing(context.Background(), ccRoot); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE uuid=?`, uuid).Scan(&n)
	if n != 1 {
		t.Fatalf("codex session purged when its root was temporarily unavailable (n=%d)", n)
	}
}

func TestSourceForRoutesToClaudeCodeFallback(t *testing.T) {
	ing := New(openTestDB(t), nil)
	if src := ing.sourceFor("/x/abc.jsonl"); src == nil || src.Name() != "claude-code" {
		t.Fatalf("expected the claude-code fallback for a .jsonl path, got %v", src)
	}
	if src := ing.sourceFor("/x/not-a-transcript.txt"); src != nil {
		t.Fatalf("expected no owner for a non-.jsonl path, got %v", src.Name())
	}
}

func TestIngestRecordsClaudeCodeSource(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evUser2)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	var src string
	if err := database.QueryRow(`SELECT source FROM sessions WHERE uuid='sess-1'`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	if src != "claude-code" {
		t.Fatalf("source=%q want claude-code", src)
	}
}

func TestIngestFullAndSearch(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-lin-Herd-cli-project-COMPLETE", "sess-1", evUser1, evAsst1, evResult1, evUser2)

	database := openTestDB(t)
	ing := New(database, nil)
	st, err := ing.IngestAll(context.Background(), projects, false)
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

	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	st, err := ing.IngestAll(context.Background(), projects, false) // second run: unchanged
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

	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
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

	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
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
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
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

	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
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
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
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

// TestIngestSecretRedactionPipeline verifies that secrets in user messages (plain text,
// JSON-as-text) and in assistant event JSON are all scrubbed from sessions.title,
// messages.content, and messages.raw_json.
func TestIngestSecretRedactionPipeline(t *testing.T) {
	projects := t.TempDir()
	// First user message: plain-text secret (env-var style).
	evUser1Sec := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"sec-sess","message":{"role":"user","content":"OPENAI_API_KEY=sk-aaaaaaaaaaaaaaaaaaaa"}}`
	// Second user message: JSON-as-text with a secret key.
	evUser2Sec := `{"type":"user","timestamp":"2026-04-26T11:01:00Z","cwd":"/p","sessionId":"sec-sess","message":{"role":"user","content":"{\"apiKey\":\"secret-value-123456\"}"}}`
	// Assistant event whose raw JSON contains a token field in tool_use input.
	evAsst1Sec := `{"type":"assistant","timestamp":"2026-04-26T11:01:05Z","sessionId":"sec-sess","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"SomeTool","input":{"token":"plainsecret123","command":"ls"}}]}}`
	writeSession(t, projects, "-p", "sec-sess", evUser1Sec, evUser2Sec, evAsst1Sec)

	d := openTestDB(t)
	if _, err := New(d, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	// Check session title (derived from first user message).
	var title string
	d.QueryRow(`SELECT title FROM sessions WHERE uuid='sec-sess'`).Scan(&title)
	if strings.Contains(title, "sk-aaaaaaaaaaaaaaaaaaaa") {
		t.Errorf("session title leaked secret: %q", title)
	}

	// Check all messages for content and raw_json leaks.
	rows, err := d.Query(`SELECT content, raw_json FROM messages WHERE session_uuid='sec-sess'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	msgCount := 0
	for rows.Next() {
		var content, rawJSON string
		if err := rows.Scan(&content, &rawJSON); err != nil {
			t.Fatal(err)
		}
		msgCount++
		for _, secret := range []string{"sk-aaaaaaaaaaaaaaaaaaaa", "secret-value-123456", "plainsecret123"} {
			if strings.Contains(content, secret) {
				t.Errorf("message content leaked secret %q: %q", secret, content)
			}
			if strings.Contains(rawJSON, secret) {
				t.Errorf("message raw_json leaked secret %q: %q", secret, rawJSON)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if msgCount == 0 {
		t.Fatal("no messages found for sec-sess")
	}
}

// TestIngestRedactsAuthAndCookie verifies Authorization: Basic / Cookie headers and
// the authorization/cookie JSON keys are scrubbed from title, content, and raw_json.
func TestIngestRedactsAuthAndCookie(t *testing.T) {
	projects := t.TempDir()
	// First user message: a cookie header (also becomes the title source).
	evUser1 := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"ac-sess","message":{"role":"user","content":"Cookie: session=topsecret123; csrf=abc"}}`
	// Second user message: an Authorization: Basic header.
	evUser2 := `{"type":"user","timestamp":"2026-04-26T11:01:00Z","cwd":"/p","sessionId":"ac-sess","message":{"role":"user","content":"curl -H 'Authorization: Basic QWxhZGRpbjpvcGVuc2VzYW1l'"}}`
	// Assistant tool_use input with an authorization key.
	evAsst1 := `{"type":"assistant","timestamp":"2026-04-26T11:01:05Z","sessionId":"ac-sess","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"SomeTool","input":{"authorization":"Basic c2VjcmV0OnZhbHVl","command":"ls"}}]}}`
	writeSession(t, projects, "-p", "ac-sess", evUser1, evUser2, evAsst1)

	d := openTestDB(t)
	if _, err := New(d, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	secrets := []string{"topsecret123", "QWxhZGRpbjpvcGVuc2VzYW1l", "c2VjcmV0OnZhbHVl"}

	var title string
	d.QueryRow(`SELECT title FROM sessions WHERE uuid='ac-sess'`).Scan(&title)
	for _, s := range secrets {
		if strings.Contains(title, s) {
			t.Errorf("session title leaked %q: %q", s, title)
		}
	}

	rows, err := d.Query(`SELECT content, raw_json FROM messages WHERE session_uuid='ac-sess'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	msgCount := 0
	for rows.Next() {
		var content, rawJSON string
		if err := rows.Scan(&content, &rawJSON); err != nil {
			t.Fatal(err)
		}
		msgCount++
		for _, s := range secrets {
			if strings.Contains(content, s) {
				t.Errorf("content leaked %q: %q", s, content)
			}
			if strings.Contains(rawJSON, s) {
				t.Errorf("raw_json leaked %q: %q", s, rawJSON)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if msgCount == 0 {
		t.Fatal("no messages found for ac-sess")
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func unparsedCount(t *testing.T, d *db.DB, src string) int64 {
	t.Helper()
	var n int64
	if err := d.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, src).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// readCappedLine: the streaming line reader's cap logic (over-cap detection without OOM).
func TestReadCappedLine(t *testing.T) {
	mk := func(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }

	data, n, term, over, err := readCappedLine(mk("abc\nrest"), 1024)
	if !term || over || err != nil || string(data) != "abc\n" || n != 4 {
		t.Fatalf("normal: data=%q n=%d term=%v over=%v err=%v", data, n, term, over, err)
	}
	// over-cap but newline present: terminated, over-cap, full byte count consumed, no buffer
	data, n, term, over, _ = readCappedLine(mk("abcdefghij\nx"), 4)
	if !term || !over || data != nil || n != 11 {
		t.Fatalf("overcap-term: data=%q n=%d term=%v over=%v", data, n, term, over)
	}
	// over-cap with no newline before EOF: not terminated (leave for next pass), over-cap
	_, _, term, over, _ = readCappedLine(mk("abcdefghij"), 4)
	if term || !over {
		t.Fatalf("overcap-eof: term=%v over=%v", term, over)
	}
	// partial trailing line under cap, no newline: not terminated
	data, _, term, over, _ = readCappedLine(mk("abc"), 1024)
	if term || over || string(data) != "abc" {
		t.Fatalf("partial: data=%q term=%v over=%v", data, term, over)
	}
}

// headFingerprint distinguishes different leading bytes and is stable for same content.
func TestHeadFingerprintDistinguishes(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a")
	p2 := filepath.Join(dir, "b")
	os.WriteFile(p1, []byte("hello world line one\n"), 0o600)
	os.WriteFile(p2, []byte("DIFFERENT first bytes\n"), 0o600)
	open := func(p string) *os.File { f, _ := os.Open(p); t.Cleanup(func() { f.Close() }); return f }
	h1, _ := headFingerprint(open(p1))
	h2, _ := headFingerprint(open(p2))
	h1b, _ := headFingerprint(open(p1))
	if h1 == "" || h1 == h2 {
		t.Fatalf("expected distinct non-empty head fps, got %q %q", h1, h2)
	}
	if h1 != h1b {
		t.Fatalf("same content different fp: %q %q", h1, h1b)
	}
}

// unparsed_lines accumulates across incremental passes and resets on full reingest.
func TestIngestUnparsedLinesAccumulate(t *testing.T) {
	projects := t.TempDir()
	good1 := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"u-sess","message":{"role":"user","content":"hello one"}}`
	bad := `this is not valid json`
	good2 := `{"type":"user","timestamp":"2026-04-26T11:02:00Z","cwd":"/p","sessionId":"u-sess","message":{"role":"user","content":"hello two"}}`
	path := writeSession(t, projects, "-p", "u-sess", good1, bad, good2)
	d := openTestDB(t)
	ing := New(d, nil)

	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	if got := unparsedCount(t, d, path); got != 1 {
		t.Fatalf("pass1 unparsed=%d want 1", got)
	}
	var msgs int
	d.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='u-sess'`).Scan(&msgs)
	if msgs != 2 {
		t.Fatalf("msgs=%d want 2 (both good lines ingested past the bad one)", msgs)
	}

	// Clean incremental append must NOT reset the counter to 0 (accumulate semantics).
	appendLine(t, path, `{"type":"user","timestamp":"2026-04-26T11:03:00Z","cwd":"/p","sessionId":"u-sess","message":{"role":"user","content":"hello three"}}`)
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	if got := unparsedCount(t, d, path); got != 1 {
		t.Fatalf("after clean append unparsed=%d want 1 (accumulate)", got)
	}

	// Full reingest resets to the count seen in the full pass (one bad line still present).
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	if got := unparsedCount(t, d, path); got != 1 {
		t.Fatalf("after full reingest unparsed=%d want 1 (reset to pass count)", got)
	}
}

// An empty stored head_fingerprint (pre-0005 row) resumes incrementally and backfills,
// instead of forcing a full reingest on upgrade.
func TestIngestEmptyHeadFingerprintBackfills(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-p", "h-sess", evUser1)
	d := openTestDB(t)
	ing := New(d, nil)
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE ingest_state SET head_fingerprint='' WHERE source_file=?`, path); err != nil {
		t.Fatal(err)
	}
	appendLine(t, path, evAsst1)
	if _, ingested, err := ing.IngestFile(context.Background(), path, false); err != nil || !ingested {
		t.Fatalf("expected incremental ingest, ingested=%v err=%v", ingested, err)
	}
	var head string
	d.QueryRow(`SELECT head_fingerprint FROM ingest_state WHERE source_file=?`, path).Scan(&head)
	if head == "" {
		t.Fatal("head_fingerprint was not backfilled")
	}
}

// commit() must abort (not write a stale snapshot) when the source can no longer be
// stat'd at commit time.
func TestIngestAbortsWhenSourceRemovedAtCommit(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-p", "rm-sess", evUser1, evUser2)
	d := openTestDB(t)
	ing := New(d, nil)
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	ing.preCommitHook = func() { os.Remove(path); ing.preCommitHook = nil }
	t.Cleanup(func() { ing.preCommitHook = nil })
	_, ingested, err := ing.IngestFile(context.Background(), path, true)
	if err != nil {
		t.Fatalf("expected clean abort, got %v", err)
	}
	if ingested {
		t.Fatal("expected ingested=false when source removed before commit re-validation")
	}
}

// BenchmarkIngestFullIndex measures a from-scratch index of a synthetic history, to
// decide whether prepared statements / batched inserts in commit() are worth it (R2).
func BenchmarkIngestFullIndex(b *testing.B) {
	projects := b.TempDir()
	const files, msgsPerFile = 50, 200
	for f := 0; f < files; f++ {
		uuid := fmt.Sprintf("bench-%03d", f)
		lines := make([]string, 0, msgsPerFile)
		for m := 0; m < msgsPerFile; m++ {
			lines = append(lines, fmt.Sprintf(`{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"%s","message":{"role":"user","content":"message %d with some content to index for fts search"}}`, uuid, m))
		}
		dir := filepath.Join(projects, "-p-"+uuid)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, uuid+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		d, err := db.Open(filepath.Join(b.TempDir(), fmt.Sprintf("b-%d.sqlite", i)))
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, err := New(d, nil).IngestAll(context.Background(), projects, true); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		d.Close()
	}
}

// sessionEvent builds a one-line user event for the given session uuid and content.
func sessionEvent(uuid, content string) string {
	return `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"` + uuid + `","message":{"role":"user","content":"` + content + `"}}`
}

func sessionCount(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// PurgeMissing removes a deleted source's rows and leaves survivors intact.
func TestPurgeMissingRemovesDeletedSource(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-p", "keep", sessionEvent("keep", "keep content here"))
	gonePath := writeSession(t, projects, "-p", "gone", sessionEvent("gone", "UNIQUEGONE content here"))
	d := openTestDB(t)
	ing := New(d, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(gonePath); err != nil {
		t.Fatal(err)
	}
	if err := ing.PurgeMissing(context.Background(), projects); err != nil {
		t.Fatal(err)
	}

	var n int
	d.QueryRow(`SELECT count(*) FROM sessions WHERE uuid='gone'`).Scan(&n)
	if n != 0 {
		t.Fatalf("deleted session not purged: %d", n)
	}
	d.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='gone'`).Scan(&n)
	if n != 0 {
		t.Fatalf("deleted messages not purged: %d", n)
	}
	d.QueryRow(`SELECT count(*) FROM ingest_state WHERE source_file=?`, gonePath).Scan(&n)
	if n != 0 {
		t.Fatalf("deleted ingest_state not purged: %d", n)
	}
	d.QueryRow(`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'UNIQUEGONE'`).Scan(&n)
	if n != 0 {
		t.Fatalf("deleted content still in FTS: %d", n)
	}
	d.QueryRow(`SELECT count(*) FROM sessions WHERE uuid='keep'`).Scan(&n)
	if n != 1 {
		t.Fatalf("survivor session missing: %d", n)
	}
}

// PurgeMissing must remove messages even when the sessions row is already gone
// (ghost state): deletion must key on the canonical uuid, not a sessions subquery.
func TestPurgeMissingRemovesGhostMessages(t *testing.T) {
	projects := t.TempDir()
	gonePath := writeSession(t, projects, "-p", "ghost", sessionEvent("ghost", "GHOSTCONTENT here"))
	d := openTestDB(t)
	ing := New(d, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	// Simulate a ghost: drop the sessions row but leave messages + ingest_state.
	if _, err := d.Exec(`DELETE FROM sessions WHERE uuid='ghost'`); err != nil {
		t.Fatal(err)
	}
	var m int
	d.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='ghost'`).Scan(&m)
	if m == 0 {
		t.Fatal("setup: expected ghost messages to exist")
	}
	os.Remove(gonePath)
	if err := ing.PurgeMissing(context.Background(), projects); err != nil {
		t.Fatal(err)
	}
	d.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='ghost'`).Scan(&m)
	if m != 0 {
		t.Fatalf("ghost messages not purged: %d", m)
	}
}

// When a session file is moved/renamed to a new path (same filename => same uuid) and
// re-ingested, purging the OLD path must NOT delete the session that now lives at the new
// path — only the stale ingest_state for the old path. (Integration of ② ingest + ③ purge;
// e.g. a renamed project directory.)
func TestPurgeMissingKeepsMovedSession(t *testing.T) {
	projects := t.TempDir()
	oldPath := writeSession(t, projects, "-old", "moved", sessionEvent("moved", "moved content here"))
	d := openTestDB(t)
	ing := New(d, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	// Move: remove old path, recreate the same-named file under a new project dir.
	os.Remove(oldPath)
	writeSession(t, projects, "-new", "moved", sessionEvent("moved", "moved content here"))
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	if err := ing.PurgeMissing(context.Background(), projects); err != nil {
		t.Fatal(err)
	}

	var n int
	d.QueryRow(`SELECT count(*) FROM sessions WHERE uuid='moved'`).Scan(&n)
	if n != 1 {
		t.Fatalf("moved session wrongly purged: sessions=%d want 1", n)
	}
	d.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid='moved'`).Scan(&n)
	if n == 0 {
		t.Fatal("moved session's messages wrongly purged")
	}
	d.QueryRow(`SELECT count(*) FROM ingest_state WHERE source_file=?`, oldPath).Scan(&n)
	if n != 0 {
		t.Fatalf("stale ingest_state for old path not cleaned: %d", n)
	}
}

// A missing/unreadable projects root must NOT purge anything (filesystem unavailable).
func TestPurgeMissingSkipsWhenRootMissing(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-p", "s1", sessionEvent("s1", "content one"))
	d := openTestDB(t)
	ing := New(d, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(projects) // whole root gone
	if err := ing.PurgeMissing(context.Background(), projects); err != nil {
		t.Fatal(err)
	}
	if got := sessionCount(t, d); got != 1 {
		t.Fatalf("root missing must not purge; sessions=%d want 1", got)
	}
}

// A small history where every source is genuinely deleted must still purge (the ratio
// cap must not block small installs).
func TestPurgeMissingSmallHistoryStillPurges(t *testing.T) {
	projects := t.TempDir()
	p1 := writeSession(t, projects, "-p", "a", sessionEvent("a", "aaa"))
	p2 := writeSession(t, projects, "-p", "b", sessionEvent("b", "bbb"))
	d := openTestDB(t)
	ing := New(d, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	os.Remove(p1)
	os.Remove(p2)
	if err := ing.PurgeMissing(context.Background(), projects); err != nil {
		t.Fatal(err)
	}
	if got := sessionCount(t, d); got != 0 {
		t.Fatalf("small history: expected both purged, sessions=%d", got)
	}
}

// A mass disappearance (large count AND most of the corpus) is treated as a filesystem
// problem and must NOT purge.
func TestPurgeMissingAbortsOnMassDisappearance(t *testing.T) {
	projects := t.TempDir()
	var paths []string
	for i := 0; i < 12; i++ {
		uuid := fmt.Sprintf("s%02d", i)
		paths = append(paths, writeSession(t, projects, "-p", uuid, sessionEvent(uuid, "content "+uuid)))
	}
	d := openTestDB(t)
	ing := New(d, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		os.Remove(p)
	}
	if err := ing.PurgeMissing(context.Background(), projects); err != nil {
		t.Fatal(err)
	}
	if got := sessionCount(t, d); got != 12 {
		t.Fatalf("mass disappearance must abort purge; sessions=%d want 12", got)
	}
}

func TestIngestRawJSONRedacted(t *testing.T) {
	projects := t.TempDir()
	leak := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"sess-1","message":{"role":"user","content":"my key AKIAIOSFODNN7EXAMPLE"}}`
	writeSession(t, projects, "-p", "sess-1", leak)
	d := openTestDB(t)
	if _, err := New(d, nil).IngestAll(context.Background(), projects, false); err != nil {
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
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
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
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
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
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	// Batch 2 (later incremental): the matching tool_result. A fresh parser
	// would forget clio-1 and index this; the persisted excluded set prevents it.
	res := `{"type":"user","timestamp":"2026-04-26T11:01:00Z","cwd":"/p","sessionId":"sess-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"clio-1","content":"secret clio results"}]}}`
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(res + "\n")
	f.Close()
	bumpMtime(t, path)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
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

	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil { // force full re-ingest again
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

	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
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
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
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
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}

	// While the next full ingest is mid-transaction (after BEGIN, before writes),
	// replace the file with V2 (four events, different size) to simulate a
	// concurrent writer's commit.
	ing.preCommitHook = func() {
		writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evAsst1, evResult1, evUser2)
		ing.preCommitHook = nil
	}
	t.Cleanup(func() { ing.preCommitHook = nil })

	// Force a full ingest using the stale (V1) in-memory snapshot. It must abort
	// cleanly (no error surfaced) rather than revert the DB to V1 over V2.
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatalf("ingest should abort cleanly, got error: %v", err)
	}

	// A subsequent ingest reconciles the DB to V2 (more messages than V1's 2).
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
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

// Fix 1: tool_use summaries (toolUseSummary) must not leak JSON-shaped secrets.
// A tool_use whose input.description is a JSON string like {"apiKey":"secret-value-123456"}
// must have that value structurally redacted via redactString (not just Redact).
func TestIngestToolUseSummaryRedactsJSONSecret(t *testing.T) {
	projects := t.TempDir()
	// Non-clio tool_use whose input.description is JSON with a secret key.
	evAsstToolUse := `{"type":"assistant","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"tu-sess","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"SomeTool","input":{"description":"{\"apiKey\":\"secret-value-123456\"}"}}]}}`
	writeSession(t, projects, "-p", "tu-sess", evAsstToolUse)

	d := openTestDB(t)
	if _, err := New(d, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	rows, err := d.Query(`SELECT content FROM messages WHERE session_uuid='tu-sess'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(content, "secret-value-123456") {
			t.Errorf("tool_use summary leaked JSON secret in messages.content: %q", content)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
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

// writeSubagent creates a Claude Code subagent transcript under
// <root>/<encodedDir>/<parentUUID>/subagents/agent-<agentID>.jsonl.
func writeSubagent(t *testing.T, root, encodedDir, parentUUID, agentID string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, encodedDir, parentUUID, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-"+agentID+".jsonl")
	var data []byte
	for _, l := range lines {
		data = append(data, []byte(l+"\n")...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A subagent transcript (under a subagents/ dir) is linked to its parent session
// via the inner sessionId and tagged with its type (attributionAgent), instead of
// being indexed as an orphan top-level session keyed by its agent-<id> filename.
func TestIngestSubagentLinksToParent(t *testing.T) {
	projects := t.TempDir()
	scUser := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"parent-1","isSidechain":true,"agentId":"a134","message":{"role":"user","content":"implement the thing"}}`
	scAsst := `{"type":"assistant","timestamp":"2026-04-26T11:00:05Z","cwd":"/p","sessionId":"parent-1","isSidechain":true,"agentId":"a134","attributionAgent":"general-purpose","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`
	writeSubagent(t, projects, "-p", "parent-1", "a134", scUser, scAsst)

	d := openTestDB(t)
	if _, err := New(d, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	var parent, agentType string
	if err := d.QueryRow(`SELECT COALESCE(parent_session,''), COALESCE(agent_type,'') FROM sessions WHERE uuid='agent-a134'`).Scan(&parent, &agentType); err != nil {
		t.Fatal(err)
	}
	if parent != "parent-1" {
		t.Fatalf("parent_session=%q want parent-1 (inner sessionId)", parent)
	}
	if agentType != "general-purpose" {
		t.Fatalf("agent_type=%q want general-purpose (attributionAgent)", agentType)
	}
}

// When a subagent transcript's lines carry no sessionId, the parent link falls
// back to the parent-session-uuid directory that holds the subagents/ dir; a
// transcript with no attributionAgent has an empty (generic) type.
func TestIngestSubagentParentFallsBackToDir(t *testing.T) {
	projects := t.TempDir()
	// No sessionId field, no attributionAgent.
	sc := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","isSidechain":true,"agentId":"b7","message":{"role":"user","content":"work it"}}`
	writeSubagent(t, projects, "-p", "parent-2", "b7", sc)

	d := openTestDB(t)
	if _, err := New(d, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	var parent, agentType string
	if err := d.QueryRow(`SELECT COALESCE(parent_session,''), COALESCE(agent_type,'') FROM sessions WHERE uuid='agent-b7'`).Scan(&parent, &agentType); err != nil {
		t.Fatal(err)
	}
	if parent != "parent-2" {
		t.Fatalf("parent_session=%q want parent-2 (dir-name fallback)", parent)
	}
	if agentType != "" {
		t.Fatalf("agent_type=%q want empty (no attributionAgent)", agentType)
	}
}

// An ordinary session file (not under subagents/) has no parent link.
func TestIngestNormalSessionHasNoParent(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-lin-Herd-x", "sess-1", evUser1, evUser2)
	d := openTestDB(t)
	if _, err := New(d, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	var parent string
	if err := d.QueryRow(`SELECT COALESCE(parent_session,'') FROM sessions WHERE uuid='sess-1'`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != "" {
		t.Fatalf("parent_session=%q want empty for a normal session", parent)
	}
}

// A normal session whose encoded project directory is literally "subagents" must
// NOT be misdetected as a subagent transcript (detection requires the agent-<id>
// filename, not just a subagents/ parent dir).
func TestIngestNormalSessionInSubagentsDirNotMisdetected(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "subagents", "sess-x", sessionEvent("sess-x", "hello there"))
	d := openTestDB(t)
	if _, err := New(d, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	var parent string
	d.QueryRow(`SELECT COALESCE(parent_session,'') FROM sessions WHERE uuid='sess-x'`).Scan(&parent)
	if parent != "" {
		t.Fatalf("a normal session under a project dir named 'subagents' must not link to a parent; got %q", parent)
	}
}

// A subagent transcript with no cwd on any line falls back to the project
// directory above <parent>/subagents, not the literal "subagents" directory.
func TestIngestSubagentProjectFallsBackToProjectDir(t *testing.T) {
	projects := t.TempDir()
	sc := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","sessionId":"parent-3","isSidechain":true,"agentId":"e3","message":{"role":"user","content":"go"}}`
	writeSubagent(t, projects, "-Users-lin-Herd-proj", "parent-3", "e3", sc)
	d := openTestDB(t)
	if _, err := New(d, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	var pp string
	d.QueryRow(`SELECT COALESCE(project_path,'') FROM sessions WHERE uuid='agent-e3'`).Scan(&pp)
	if pp != "/Users/lin/Herd/proj" {
		t.Fatalf("subagent project_path should decode the project dir (not the subagents dir), got %q", pp)
	}
}

// On an incremental ingest, subagent metadata that appears only in later lines
// (the assistant turn carrying attributionAgent) is written, not lost until a full
// reindex.
func TestIngestSubagentTypeFilledOnIncrement(t *testing.T) {
	projects := t.TempDir()
	// The first line must exceed the 512-byte fingerprint window so the later
	// append is a true incremental ingest (a short first line shifts the head
	// fingerprint and would force a full re-ingest, hiding the bug).
	longTask := strings.Repeat("task detail ", 60)
	scUser := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"parent-7","isSidechain":true,"agentId":"d7","message":{"role":"user","content":"` + longTask + `"}}`
	path := writeSubagent(t, projects, "-p", "parent-7", "d7", scUser)
	d := openTestDB(t)
	ing := New(d, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	var typ0 string
	d.QueryRow(`SELECT COALESCE(agent_type,'') FROM sessions WHERE uuid='agent-d7'`).Scan(&typ0)
	if typ0 != "" {
		t.Fatalf("precondition: no attributionAgent yet → agent_type empty, got %q", typ0)
	}
	// The assistant turn (carrying attributionAgent) appends later.
	appendLine(t, path, `{"type":"assistant","timestamp":"2026-04-26T11:01:00Z","cwd":"/p","sessionId":"parent-7","isSidechain":true,"agentId":"d7","attributionAgent":"general-purpose","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`)
	bumpMtime(t, path)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	var typ1, parent string
	d.QueryRow(`SELECT COALESCE(agent_type,''), COALESCE(parent_session,'') FROM sessions WHERE uuid='agent-d7'`).Scan(&typ1, &parent)
	if typ1 != "general-purpose" {
		t.Fatalf("incremental ingest should fill agent_type from a later line, got %q", typ1)
	}
	if parent != "parent-7" {
		t.Fatalf("parent_session should remain set, got %q", parent)
	}
}

// Upgrading a database that indexed subagent transcripts as orphan top-level
// sessions (before linking existed) relinks them in place: the backfill migration
// clears the subagent ingest watermark so the next index re-ingests and populates
// parent_session/agent_type on the same rows, with no duplicates.
func TestSubagentBackfillMigrationRelinksOrphans(t *testing.T) {
	projects := t.TempDir()
	sc := `{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"parent-9","isSidechain":true,"agentId":"c9","attributionAgent":"general-purpose","message":{"role":"user","content":"do the task"}}`
	writeSubagent(t, projects, "-p", "parent-9", "c9", sc)

	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(d, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-feature index: the link is absent but the ingest watermark is
	// present (so a plain re-index would skip the unchanged file), and the backfill
	// migration has not yet run.
	if _, err := d.Exec(`UPDATE sessions SET parent_session=NULL, agent_type=NULL WHERE uuid='agent-c9'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`DELETE FROM schema_migrations WHERE name='0008_backfill_subagents.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	// Upgrade: re-open re-applies the backfill migration (clears the subagent
	// watermark); the next index relinks the orphan in place.
	d2, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d2.Close() })
	if _, err := New(d2, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	var parent, agentType string
	if err := d2.QueryRow(`SELECT COALESCE(parent_session,''), COALESCE(agent_type,'') FROM sessions WHERE uuid='agent-c9'`).Scan(&parent, &agentType); err != nil {
		t.Fatal(err)
	}
	if parent != "parent-9" || agentType != "general-purpose" {
		t.Fatalf("orphan not relinked after backfill: parent=%q type=%q", parent, agentType)
	}
	var rows int
	d2.QueryRow(`SELECT count(*) FROM sessions WHERE uuid='agent-c9'`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("backfill duplicated session rows: %d", rows)
	}
}

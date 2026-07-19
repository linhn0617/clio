package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/model"
)

const geminiTestdataRoot = "testdata/gemini/tmp"
const geminiSampleUUID = "4ac5c3df-ca64-4c94-9ad8-d6792fcda807"

func newGeminiTestIngester(t *testing.T) (*Ingester, *db.DB) {
	t.Helper()
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: geminiTestdataRoot})
	return ing, database
}

// ---- Task 3.1: discovery / ownership ----

func TestGeminiOwnsMainSessionFile(t *testing.T) {
	s := geminiSource{root: geminiTestdataRoot}
	p := filepath.Join(geminiTestdataRoot, "gemini-sample/chats/session-2026-07-17T14-18-4ac5c3df.jsonl")
	if !s.Owns(p) {
		t.Fatalf("expected gemini adapter to own %q", p)
	}
}

// TestGeminiOwnsBeforeClaudeCodeFallback proves the gemini adapter is
// consulted before the claude-code fallback (which owns any .jsonl) once
// registered via AddSource (spec: "the gemini adapter SHALL own it, and the
// claude-code fallback SHALL NOT").
func TestGeminiOwnsBeforeClaudeCodeFallback(t *testing.T) {
	ing := New(openTestDB(t), nil)
	ing.AddSource(geminiSource{root: geminiTestdataRoot})
	p := filepath.Join(geminiTestdataRoot, "gemini-sample/chats/session-2026-07-17T14-18-4ac5c3df.jsonl")
	src := ing.sourceFor(p)
	if src == nil || src.Name() != model.SourceGemini {
		t.Fatalf("sourceFor(%q) = %v, want gemini", p, src)
	}
}

func TestGeminiOwnsNestedChild(t *testing.T) {
	s := geminiSource{root: geminiTestdataRoot}
	p := filepath.Join(geminiTestdataRoot, "nested-parent/chats/66666666-ffff-4fff-8fff-ffffffffffff/77777777-0000-4000-8000-000000000000.jsonl")
	if !s.Owns(p) {
		t.Fatalf("expected gemini adapter to own nested child %q", p)
	}
}

// TestGeminiOwnsNoneForOldOrNonChatsLayouts covers the spec scenario "Old and
// non-chats layouts own no files": a ≤0.1.9 sha256-hash project dir with no
// chats/ (logs.json), and a checkpoint-*.json — none owned (not an error).
func TestGeminiOwnsNoneForOldOrNonChatsLayouts(t *testing.T) {
	s := geminiSource{root: geminiTestdataRoot}
	cases := []string{
		filepath.Join(geminiTestdataRoot, "old-layout-no-chats/logs.json"),
		filepath.Join(geminiTestdataRoot, "checkpoint-project/checkpoint-mytag.json"),
		filepath.Join(geminiTestdataRoot, "9455af41ba476b1fb97d1518ca72a11e9f46da7307bac977787f0bec6e7585f8/logs.json"),
	}
	for _, p := range cases {
		if s.Owns(p) {
			t.Errorf("expected gemini adapter to NOT own %q", p)
		}
	}
}

func TestGeminiOwnsRejectsOutsideRoot(t *testing.T) {
	s := geminiSource{root: geminiTestdataRoot}
	if s.Owns("/some/other/place/chats/session-x.jsonl") {
		t.Fatal("expected gemini adapter to not own a path outside its root")
	}
}

// TestGeminiIngestSkipsOldAndNonChatsLayouts is the end-to-end version of
// TestGeminiOwnsNoneForOldOrNonChatsLayouts (task 3.3 "asserted, not
// errored"): a full IngestAll over the fixture root must not error and must
// not index anything for the old-layout/logs.json/checkpoint files.
func TestGeminiIngestSkipsOldAndNonChatsLayouts(t *testing.T) {
	ing, database := newGeminiTestIngester(t)
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not error on old/non-chats layouts: %v", err)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE source_file LIKE ?`, "%old-layout-no-chats%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 sessions indexed from the old-layout logs.json, got %d", n)
	}
}

// ---- Task 3.2: projects.json inversion ----

func TestGeminiProjectIDFromChatsParentDir(t *testing.T) {
	p := filepath.Join(geminiTestdataRoot, "gemini-sample/chats/session-2026-07-17T14-18-4ac5c3df.jsonl")
	if got, want := geminiProjectID(p), "gemini-sample"; got != want {
		t.Fatalf("geminiProjectID(%q) = %q, want %q", p, got, want)
	}
}

func TestGeminiProjectIDForNestedChild(t *testing.T) {
	p := filepath.Join(geminiTestdataRoot, "nested-parent/chats/66666666-ffff-4fff-8fff-ffffffffffff/77777777-0000-4000-8000-000000000000.jsonl")
	if got, want := geminiProjectID(p), "nested-parent"; got != want {
		t.Fatalf("geminiProjectID(%q) = %q, want %q (a nested child maps to the SAME projectId as its parent)", p, got, want)
	}
}

func TestGeminiProjectPathResolvesViaProjectsJSON(t *testing.T) {
	got := geminiProjectPath(geminiTestdataRoot, "gemini-sample")
	want := "/private/tmp/claude-502/-Users-lin/8faa745e-16ca-4450-a9d1-24ec7b6c3900/scratchpad/gemini-sample"
	if got != want {
		t.Fatalf("geminiProjectPath = %q, want %q", got, want)
	}
}

func TestGeminiProjectPathEmptyWhenNoEntry(t *testing.T) {
	if got := geminiProjectPath(geminiTestdataRoot, "no-such-project-id"); got != "" {
		t.Fatalf("geminiProjectPath for an unmapped projectId = %q, want \"\" (indexed, unattributed)", got)
	}
}

func TestGeminiProjectPathEmptyWhenProjectsJSONMissing(t *testing.T) {
	if got := geminiProjectPath(t.TempDir()+"/tmp", "gemini-sample"); got != "" {
		t.Fatalf("geminiProjectPath with no projects.json = %q, want \"\"", got)
	}
}

// ---- Task 4.1: session identity from metadata ----

func TestGeminiSessionIDFromPathReadsMetadataLine(t *testing.T) {
	s := geminiSource{}
	p := filepath.Join(geminiTestdataRoot, "gemini-sample/chats/session-2026-07-17T14-18-4ac5c3df.jsonl")
	if got := s.SessionIDFromPath(p); got != geminiSampleUUID {
		t.Fatalf("SessionIDFromPath(%q) = %q, want the full metadata uuid %q (not the 8-char filename fragment)", p, got, geminiSampleUUID)
	}
}

func TestGeminiSessionIDFromPathEmptyOnMissingFile(t *testing.T) {
	s := geminiSource{}
	if got := s.SessionIDFromPath("/no/such/file.jsonl"); got != "" {
		t.Fatalf("SessionIDFromPath for a missing file = %q, want \"\"", got)
	}
}

// ---- Task 4.2/4.4/4.5: real-sample ingest (observed shapes only) ----

// TestGeminiIngestRealSampleWrapperOnly is the OBSERVED fixture: the real
// v0.51.0 sample's only user message is a <session_context>-only wrapper, so
// it produces a session with source=gemini and zero turns/messages, per the
// spec scenario "A session_context-only user record is not a turn".
func TestGeminiIngestRealSampleWrapperOnly(t *testing.T) {
	ing, database := newGeminiTestIngester(t)
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	var src, pp string
	var turns int
	if err := database.QueryRow(`SELECT source, COALESCE(project_path,''), turn_count FROM sessions WHERE uuid=?`, geminiSampleUUID).Scan(&src, &pp, &turns); err != nil {
		t.Fatalf("gemini sample session not indexed: %v", err)
	}
	if src != model.SourceGemini {
		t.Fatalf("source=%q want gemini", src)
	}
	if turns != 0 {
		t.Fatalf("turn_count=%d want 0 (the only user message is session_context-wrapper-only)", turns)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, geminiSampleUUID).Scan(&n)
	if n != 0 {
		t.Fatalf("messages=%d want 0", n)
	}
}

// TestGeminiIngestAssistantMessage is SYNTHESIZED (real sample has no
// assistant turn): a user question + a gemini reply are indexed as
// user/assistant messages under source=gemini, with the session titled from
// the user text, and thoughts/toolCalls left unextracted (v1).
func TestGeminiIngestAssistantMessage(t *testing.T) {
	ing, database := newGeminiTestIngester(t)
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	const uuid = "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	var turns int
	var title string
	if err := database.QueryRow(`SELECT turn_count, COALESCE(title,'') FROM sessions WHERE uuid=?`, uuid).Scan(&turns, &title); err != nil {
		t.Fatalf("assistant-msg session not indexed: %v", err)
	}
	if turns != 1 {
		t.Fatalf("turn_count=%d want 1", turns)
	}
	if title != "what is 2+2" {
		t.Fatalf("title=%q want %q", title, "what is 2+2")
	}
	var userContent, asstContent, asstRaw string
	database.QueryRow(`SELECT content FROM messages WHERE session_uuid=? AND role='user'`, uuid).Scan(&userContent)
	if userContent != "what is 2+2" {
		t.Fatalf("user content=%q want %q", userContent, "what is 2+2")
	}
	database.QueryRow(`SELECT content, raw_json FROM messages WHERE session_uuid=? AND role='assistant'`, uuid).Scan(&asstContent, &asstRaw)
	if asstContent != "2+2 is 4." {
		t.Fatalf("assistant content=%q want %q", asstContent, "2+2 is 4.")
	}
	// v1 does not extract thoughts/toolCalls into separate messages, but they
	// remain in the message's raw_json (spec: "thoughts and toolCalls are not
	// extracted in v1").
	if !strings.Contains(asstRaw, "thoughts") {
		t.Fatalf("assistant raw_json missing unextracted thoughts field: %q", asstRaw)
	}
	var thinkingCount, toolUseCount int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=? AND role='thinking'`, uuid).Scan(&thinkingCount)
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=? AND role='tool_use'`, uuid).Scan(&toolUseCount)
	if thinkingCount != 0 || toolUseCount != 0 {
		t.Fatalf("expected no thinking/tool_use messages extracted in v1, got thinking=%d tool_use=%d", thinkingCount, toolUseCount)
	}
}

// TestGeminiIngestTwoSetLastWriterWins is the spec scenario "The last $set
// wins": a second $set edits the first message and adds a reply; only the
// final state must be indexed, with no leftover/duplicated messages.
func TestGeminiIngestTwoSetLastWriterWins(t *testing.T) {
	ing, database := newGeminiTestIngester(t)
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	const uuid = "22222222-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&n)
	if n != 2 {
		t.Fatalf("messages=%d want 2 (only the second $set's array, no leftovers)", n)
	}
	var userContent string
	database.QueryRow(`SELECT content FROM messages WHERE session_uuid=? AND role='user'`, uuid).Scan(&userContent)
	if userContent != "edited final question" {
		t.Fatalf("user content=%q want %q (the second $set's edit)", userContent, "edited final question")
	}
	var found int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=? AND content='first draft question'`, uuid).Scan(&found)
	if found != 0 {
		t.Fatalf("the first $set's superseded message must not survive, found %d", found)
	}
}

// TestGeminiIngestRedactsSecretInContent is the spec scenario "A secret in
// Gemini content is redacted": the shared redactString/redactJSON machinery
// must run on Gemini message content and raw_json, same as every other
// source (design.md §3 "Redaction reuse").
func TestGeminiIngestRedactsSecretInContent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "secret-proj", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-18T10-00-aaaa1234.jsonl")
	const secret = "sk-abcdefghijklmnopqrstuvwxyz123456"
	content := `{"sessionId":"aaaaaaaa-1234-4234-8234-123456789012","startTime":"2026-07-18T18:00:00.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-18T18:00:01.000Z","type":"user","content":[{"text":"my key is ` + secret + `"}]}],"lastUpdated":"2026-07-18T18:00:01.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}

	const uuid = "aaaaaaaa-1234-4234-8234-123456789012"
	var msgContent, raw string
	if err := database.QueryRow(`SELECT content, raw_json FROM messages WHERE session_uuid=? AND role='user'`, uuid).Scan(&msgContent, &raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msgContent, secret) {
		t.Fatalf("content still contains the secret: %q", msgContent)
	}
	if !strings.Contains(msgContent, "[REDACTED:api-key]") {
		t.Fatalf("content=%q, want it to contain the redaction marker", msgContent)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("raw_json still contains the secret: %q", raw)
	}
}

// TestGeminiIngestBareAppendSkippedAndCounted is the spec scenario
// "Unobserved record shapes are skipped and counted, not replayed": a bare
// (non-$set) MessageRecord line is skipped+warned+counted unparsed, while
// the file's $set-derived message is still indexed.
func TestGeminiIngestBareAppendSkippedAndCounted(t *testing.T) {
	ing, database := newGeminiTestIngester(t)
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	const uuid = "33333333-cccc-4ccc-8ccc-cccccccccccc"
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&n)
	if n != 1 {
		t.Fatalf("messages=%d want 1 (only the $set-carried message; the bare append is skipped)", n)
	}
	var unparsed int64
	if err := database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`,
		filepath.Join(geminiTestdataRoot, "bare-append/chats/session-2026-07-18T10-00-3333cccc.jsonl")).Scan(&unparsed); err != nil {
		t.Fatal(err)
	}
	if unparsed < 1 {
		t.Fatalf("unparsed_lines=%d want >=1 (the bare-append line must be counted)", unparsed)
	}
}

// TestGeminiIngestRewindSkippedAndCounted is the $rewindTo half of the same
// spec scenario as the bare-append test above.
func TestGeminiIngestRewindSkippedAndCounted(t *testing.T) {
	ing, database := newGeminiTestIngester(t)
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	const uuid = "44444444-dddd-4ddd-8ddd-dddddddddddd"
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&n)
	if n != 1 {
		t.Fatalf("messages=%d want 1 (the $rewindTo record is skipped, not replayed, in v1)", n)
	}
	var unparsed int64
	if err := database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`,
		filepath.Join(geminiTestdataRoot, "rewind/chats/session-2026-07-18T10-00-4444dddd.jsonl")).Scan(&unparsed); err != nil {
		t.Fatal(err)
	}
	if unparsed < 1 {
		t.Fatalf("unparsed_lines=%d want >=1 ($rewindTo must be counted)", unparsed)
	}
}

// TestGeminiIngestUnrecognizedAssistantContentSkipped is the spec scenario
// "An assistant message with an unrecognized content shape is not indexed
// empty": a gemini-type message with empty content[] yields no text and must
// be skipped+counted, never indexed as an empty message.
func TestGeminiIngestUnrecognizedAssistantContentSkipped(t *testing.T) {
	ing, database := newGeminiTestIngester(t)
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	const uuid = "55555555-eeee-4eee-8eee-eeeeeeeeeeee"
	var asstCount int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=? AND role='assistant'`, uuid).Scan(&asstCount)
	if asstCount != 0 {
		t.Fatalf("assistant messages=%d want 0 (empty content must not be indexed as an empty message)", asstCount)
	}
	var userCount int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=? AND role='user'`, uuid).Scan(&userCount)
	if userCount != 1 {
		t.Fatalf("user messages=%d want 1 (the normal user turn must still be indexed)", userCount)
	}
}

// ---- Task 4.3: unusable $set aborts the pass (P1) ----

// TestGeminiUnusableSetAbortsPreservingPriorState is the spec scenario "An
// over-cap $set aborts and preserves the prior index": once a session is
// indexed, a later $set that fails to parse must abort the whole pass —
// nothing committed, prior rows untouched, and the stored watermark must not
// advance (so doctor's lag check would keep flagging the file).
func TestGeminiUnusableSetAbortsPreservingPriorState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bad-set", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-18T10-00-8888ffff.jsonl")
	good := `{"sessionId":"88888888-ffff-4fff-8fff-ffffffffffff","projectHash":"deadbeef","startTime":"2026-07-18T16:00:00.000Z","lastUpdated":"2026-07-18T16:00:01.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-18T16:00:01.000Z","type":"user","content":[{"text":"question before bad set"}]},{"id":"msgB","timestamp":"2026-07-18T16:00:02.000Z","type":"gemini","content":[{"text":"answer before bad set"}]}],"lastUpdated":"2026-07-18T16:00:02.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	const uuid = "88888888-ffff-4fff-8fff-ffffffffffff"
	var before int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&before)
	if before != 2 {
		t.Fatalf("baseline messages=%d want 2", before)
	}
	var offsetBefore int64
	if err := database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file=?`, path).Scan(&offsetBefore); err != nil {
		t.Fatal(err)
	}

	// True append: an IDENTIFIED $set (valid JSON, has the "$set" key) whose
	// "messages" value is the wrong shape (a string, not an array) — this is
	// what "unparsable" means here (design.md §3 P1): the record is
	// recognizably an attempted $set, but unusable. A line that isn't even
	// syntactically valid JSON at all is deliberately NOT this scenario — it
	// can't be identified as a $set attempt, so it falls into the ordinary
	// unobserved-shape skip+count bucket instead (ingest.go rationale: only
	// an IDENTIFIED-but-broken $set aborts, per errGeminiUnusableSet).
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"$set":{"messages":"not an array"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	bumpMtime(t, path)

	fiAfterWrite, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// The ingest pass itself must not hard-fail the run.
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on an unusable $set (per-file abort only): %v", err)
	}

	var after int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&after)
	if after != before {
		t.Fatalf("messages after unusable $set=%d want unchanged %d (prior state must be preserved verbatim)", after, before)
	}
	var offsetAfter int64
	if err := database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file=?`, path).Scan(&offsetAfter); err != nil {
		t.Fatal(err)
	}
	if offsetAfter != offsetBefore {
		t.Fatalf("last_byte_offset after unusable $set=%d want unchanged %d (watermark must not advance)", offsetAfter, offsetBefore)
	}
	if fiAfterWrite.Size() <= offsetAfter {
		t.Fatalf("file size (%d) must exceed the stored offset (%d) after the abort, so doctor's lag check would flag it", fiAfterWrite.Size(), offsetAfter)
	}

	// Spec: "the failure SHALL be counted as unparsed" — the aborted pass must
	// durably bump unparsed_lines even though nothing was committed.
	var unparsed int64
	if err := database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed); err != nil {
		t.Fatal(err)
	}
	if unparsed != 1 {
		t.Fatalf("unparsed_lines after unusable $set=%d want 1 (the abort must be counted)", unparsed)
	}

	// A re-pass over the SAME broken bytes (file unchanged) must not re-abort
	// and re-count: the failing size/mtime were recorded, so classifyChange
	// skips it until the file actually changes.
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed); err != nil {
		t.Fatal(err)
	}
	if unparsed != 1 {
		t.Fatalf("unparsed_lines after unchanged re-pass=%d want still 1 (no per-pass inflation)", unparsed)
	}
	database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file=?`, path).Scan(&offsetAfter)
	if offsetAfter != offsetBefore {
		t.Fatalf("last_byte_offset after unchanged re-pass=%d want still %d", offsetAfter, offsetBefore)
	}

	// Recovery: appending a GOOD $set after the broken one re-triggers a full
	// replay (size changed) and must succeed, replacing prior state and
	// resetting unparsed_lines to that pass's own count (1: the broken $set
	// line is now an in-pass... no — the broken $set still aborts the pass).
	// A broken $set stays broken (deterministic), so recovery only happens if
	// gemini-cli itself appends a later good $set — but the replay still has
	// to traverse the broken line and abort. This asserts the file stays
	// aborted-and-flagged rather than half-ingesting past the broken record.
	f2, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f2.WriteString(`{"$set":{"messages":[{"id":"msgC","timestamp":"2026-07-18T16:00:03.000Z","type":"user","content":[{"text":"after the broken set"}]}],"lastUpdated":"2026-07-18T16:00:03.000Z"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f2.Close()
	bumpMtime(t, path)
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&after)
	if after != before {
		t.Fatalf("messages after good-$set-after-broken-$set=%d want still %d (the broken record still aborts the whole replay)", after, before)
	}
	if err := database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed); err != nil {
		t.Fatal(err)
	}
	if unparsed != 2 {
		t.Fatalf("unparsed_lines after second aborted pass=%d want 2 (each distinct failing state counts once)", unparsed)
	}
}

// TestGeminiUnparsableLineMentioningSetAbortsPreservingPriorState is the
// line-level half of "An unusable Gemini state record aborts the pass"
// (codex re-review finding): a line that is not even syntactically valid
// JSON cannot be inspected for a "$set" key, so it might have BEEN a $set
// carrying the entire conversation state. It must abort the whole pass —
// same contract as an identified-but-unparsable $set — never skip+continue.
// This fixture's broken line happens to contain the substring "$set" to
// prove the abort isn't a content-sniffing heuristic that only fires on
// recognizable fragments.
func TestGeminiUnparsableLineMentioningSetAbortsPreservingPriorState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bad-line-set-hint", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-18T10-00-cccc1111.jsonl")
	good := `{"sessionId":"cccccccc-1111-4111-8111-111111111111","projectHash":"deadbeef","startTime":"2026-07-18T21:00:00.000Z","lastUpdated":"2026-07-18T21:00:01.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-18T21:00:01.000Z","type":"user","content":[{"text":"question before broken line"}]}],"lastUpdated":"2026-07-18T21:00:01.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	const uuid = "cccccccc-1111-4111-8111-111111111111"
	var before int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&before)
	if before != 1 {
		t.Fatalf("baseline messages=%d want 1", before)
	}
	var offsetBefore int64
	if err := database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file=?`, path).Scan(&offsetBefore); err != nil {
		t.Fatal(err)
	}

	// Not syntactically valid JSON at all (trailing garbage after the closing
	// brace) — cannot be identified as $set-or-not, but it visibly mentions
	// "$set" to prove content isn't what triggers the abort.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"$set": this is not json}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	bumpMtime(t, path)

	fiAfterWrite, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on an unparsable line (per-file abort only): %v", err)
	}

	var after int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&after)
	if after != before {
		t.Fatalf("messages after unparsable line=%d want unchanged %d (prior state must be preserved)", after, before)
	}
	var offsetAfter int64
	if err := database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file=?`, path).Scan(&offsetAfter); err != nil {
		t.Fatal(err)
	}
	if offsetAfter != offsetBefore {
		t.Fatalf("last_byte_offset after unparsable line=%d want unchanged %d (watermark must not advance)", offsetAfter, offsetBefore)
	}
	if fiAfterWrite.Size() <= offsetAfter {
		t.Fatalf("file size (%d) must exceed the stored offset (%d) after the abort, so doctor's lag check would flag it", fiAfterWrite.Size(), offsetAfter)
	}
	var unparsed int64
	if err := database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed); err != nil {
		t.Fatal(err)
	}
	if unparsed != 1 {
		t.Fatalf("unparsed_lines after unparsable line=%d want 1 (the abort must be counted)", unparsed)
	}
}

// TestGeminiUnparsableLineWithoutSetHintAbortsPreservingPriorState is the
// sibling of the test above with a broken line that does NOT mention "$set"
// anywhere: fail-closed applies uniformly to any line that fails to parse as
// JSON, regardless of its content, because content can't be inspected once
// json.Unmarshal has already failed.
func TestGeminiUnparsableLineWithoutSetHintAbortsPreservingPriorState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bad-line-no-hint", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-18T10-00-dddd2222.jsonl")
	good := `{"sessionId":"dddddddd-2222-4222-8222-222222222222","projectHash":"deadbeef","startTime":"2026-07-18T22:00:00.000Z","lastUpdated":"2026-07-18T22:00:01.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-18T22:00:01.000Z","type":"user","content":[{"text":"question before broken line"}]}],"lastUpdated":"2026-07-18T22:00:01.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	const uuid = "dddddddd-2222-4222-8222-222222222222"
	var before int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&before)
	if before != 1 {
		t.Fatalf("baseline messages=%d want 1", before)
	}
	var offsetBefore int64
	if err := database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file=?`, path).Scan(&offsetBefore); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`not json at all, no braces` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	bumpMtime(t, path)

	fiAfterWrite, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on an unparsable line (per-file abort only): %v", err)
	}

	var after int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&after)
	if after != before {
		t.Fatalf("messages after unparsable line=%d want unchanged %d (fail-closed regardless of content)", after, before)
	}
	var offsetAfter int64
	if err := database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file=?`, path).Scan(&offsetAfter); err != nil {
		t.Fatal(err)
	}
	if offsetAfter != offsetBefore {
		t.Fatalf("last_byte_offset after unparsable line=%d want unchanged %d (watermark must not advance, fail-closed regardless of content)", offsetAfter, offsetBefore)
	}
	if fiAfterWrite.Size() <= offsetAfter {
		t.Fatalf("file size (%d) must exceed the stored offset (%d) after the abort, so doctor's lag check would flag it", fiAfterWrite.Size(), offsetAfter)
	}
	var unparsed int64
	if err := database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed); err != nil {
		t.Fatal(err)
	}
	if unparsed != 1 {
		t.Fatalf("unparsed_lines after unparsable line=%d want 1 (the abort must be counted)", unparsed)
	}
}

// ---- Task 5.1: nested files, flat indexing ----

func TestGeminiNestedChildIndexedFlatNoParentLink(t *testing.T) {
	ing, database := newGeminiTestIngester(t)
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	const childUUID = "77777777-0000-4000-8000-000000000000"
	var pp, parent, agentType string
	var turns int
	if err := database.QueryRow(`SELECT COALESCE(project_path,''), COALESCE(parent_session,''), COALESCE(agent_type,''), turn_count FROM sessions WHERE uuid=?`, childUUID).Scan(&pp, &parent, &agentType, &turns); err != nil {
		t.Fatalf("nested child not indexed: %v", err)
	}
	if parent != "" {
		t.Fatalf("parent_session=%q want empty (v1: no parent link)", parent)
	}
	if agentType != "" {
		t.Fatalf("agent_type=%q want empty (v1: unobserved)", agentType)
	}
	if turns != 1 {
		t.Fatalf("turn_count=%d want 1 (its own content is searchable like any session)", turns)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, childUUID).Scan(&n)
	if n != 2 {
		t.Fatalf("messages=%d want 2", n)
	}
}

func TestGeminiMainSessionHasNoParent(t *testing.T) {
	ing, database := newGeminiTestIngester(t)
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	var parent string
	database.QueryRow(`SELECT COALESCE(parent_session,'') FROM sessions WHERE uuid=?`, geminiSampleUUID).Scan(&parent)
	if parent != "" {
		t.Fatalf("main session parent_session=%q want empty", parent)
	}
}

// ---- Task 5.2: purge resolves uuid via the DB when metadata is unreadable ----

func TestGeminiPurgeDeletedFileResolvesUUIDFromDB(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "purge-me", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-18T10-00-9999aaaa.jsonl")
	content := `{"sessionId":"99999999-aaaa-4aaa-8aaa-aaaaaaaaaaaa","startTime":"2026-07-18T17:00:00.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-18T17:00:01.000Z","type":"user","content":[{"text":"to be purged"}]}],"lastUpdated":"2026-07-18T17:00:01.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	const uuid = "99999999-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE uuid=?`, uuid).Scan(&n)
	if n != 1 {
		t.Fatalf("session not indexed before purge test, n=%d", n)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := ing.PurgeMissing(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}

	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE uuid=?`, uuid).Scan(&n)
	if n != 0 {
		t.Fatalf("session rows after purge=%d want 0 (deleted Gemini file, metadata unreadable, uuid resolved via sessions.source_file)", n)
	}
	var msgN int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&msgN)
	if msgN != 0 {
		t.Fatalf("message rows after purge=%d want 0", msgN)
	}
}

// ---- Task 1.2 fake-third-source style acceptance, gemini-specific ----

// TestGeminiWholeFileReplayFlag pins the Source-interface contract (task
// 2.1): the gemini adapter is a whole-file-replay source.
func TestGeminiWholeFileReplayFlag(t *testing.T) {
	if !(geminiSource{}).WholeFileReplay() {
		t.Fatal("geminiSource.WholeFileReplay() = false, want true")
	}
}

func TestNewWithBuiltinSourcesRegistersGeminiWhenDirExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	ing := NewWithBuiltinSources(openTestDB(t), nil)
	var have bool
	for _, s := range ing.sources {
		if s.Name() == model.SourceGemini {
			have = true
		}
	}
	if !have {
		t.Fatal("expected gemini source registered when ~/.gemini/tmp exists")
	}
}

func TestNewWithBuiltinSourcesSkipsGeminiWhenDirMissing(t *testing.T) {
	home := t.TempDir() // .gemini/tmp never created
	t.Setenv("HOME", home)
	ing := NewWithBuiltinSources(openTestDB(t), nil)
	for _, s := range ing.sources {
		if s.Name() == model.SourceGemini {
			t.Fatal("gemini source must not be registered when ~/.gemini/tmp is absent")
		}
	}
}

// ---- Task 6.2: the over-cap half of "over-cap/unparsable $set abort" ----
// (the unparsable half is TestGeminiUnusableSetAbortsPreservingPriorState
// above). The oversized line is generated at runtime, not checked into
// testdata, to avoid a multi-MB fixture file.

// TestGeminiOverCapSetAbortsPreservingPriorState is the spec scenario "An
// over-cap $set aborts and preserves the prior index", the over-cap half:
// a line exceeding the shared 16 MiB readCappedLine cap cannot even be
// inspected, so it is conservatively treated as an unusable $set and the
// whole pass aborts, same as an identified-but-unparsable $set.
func TestGeminiOverCapSetAbortsPreservingPriorState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "over-cap-proj", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-18T10-00-bbbb5678.jsonl")
	good := `{"sessionId":"bbbbbbbb-5678-4678-8678-567812345678","startTime":"2026-07-18T19:00:00.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-18T19:00:01.000Z","type":"user","content":[{"text":"question before oversized set"}]}],"lastUpdated":"2026-07-18T19:00:01.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	const uuid = "bbbbbbbb-5678-4678-8678-567812345678"
	var before int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&before)
	if before != 1 {
		t.Fatalf("baseline messages=%d want 1", before)
	}

	// A $set line whose bytes exceed maxLineBytes (16 MiB, incremental.go).
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	huge := `{"$set":{"messages":[{"id":"msgB","timestamp":"2026-07-18T19:00:02.000Z","type":"user","content":[{"text":"` +
		strings.Repeat("x", maxLineBytes+1024) + `"}]}],"lastUpdated":"2026-07-18T19:00:02.000Z"}}`
	if _, err := f.WriteString(huge + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	bumpMtime(t, path)

	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on an over-cap $set (per-file abort only): %v", err)
	}

	var after int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&after)
	if after != before {
		t.Fatalf("messages after over-cap $set=%d want unchanged %d", after, before)
	}
	// The aborted pass is durably counted as unparsed (spec).
	var unparsed int64
	if err := database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed); err != nil {
		t.Fatal(err)
	}
	if unparsed != 1 {
		t.Fatalf("unparsed_lines after over-cap $set=%d want 1", unparsed)
	}
}

// ---- Task 6.3: idempotency crux (design.md §4) ----

// TestGeminiIdempotencyAcrossReingest is the spec's idempotency crux: ingest
// a fixture, append a $set that edits an earlier message, re-ingest, and
// assert the DB rows equal a from-scratch ingest of the final bytes — no
// duplicate rows, no stale rows, correct turn_count. It also covers the
// "re-ingesting unchanged then changed bytes is idempotent" scenario's
// unchanged-is-a-no-op half.
func TestGeminiIdempotencyAcrossReingest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "idempotent-proj", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-18T10-00-cccc9012.jsonl")
	const uuid = "cccccccc-9012-4012-8012-901234567890"
	firstSet := `{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-18T20:00:01.000Z","type":"user","content":[{"text":"first version of the question"}]}],"lastUpdated":"2026-07-18T20:00:01.000Z"}}` + "\n"
	metaLine := `{"sessionId":"` + uuid + `","startTime":"2026-07-18T20:00:00.000Z","kind":"main"}` + "\n"
	if err := os.WriteFile(path, []byte(metaLine+firstSet), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	// Re-ingest unchanged: a no-op (classifyChange -> changeSkip, no reparse).
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}

	secondSet := `{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-18T20:00:01.000Z","type":"user","content":[{"text":"edited final question"}]},{"id":"msgB","timestamp":"2026-07-18T20:00:03.000Z","type":"gemini","content":[{"text":"the answer"}]}],"lastUpdated":"2026-07-18T20:00:03.000Z"}}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(secondSet); err != nil {
		t.Fatal(err)
	}
	f.Close()
	bumpMtime(t, path)
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}

	var turns int
	database.QueryRow(`SELECT turn_count FROM sessions WHERE uuid=?`, uuid).Scan(&turns)
	if turns != 1 {
		t.Fatalf("turn_count=%d want 1 (one user turn; the assistant reply doesn't count as a user turn)", turns)
	}

	gotRows := geminiMessageRows(t, database, uuid)

	// From-scratch: a brand new DB ingesting only the final bytes.
	freshRoot := t.TempDir()
	freshDir := filepath.Join(freshRoot, "idempotent-proj", "chats")
	if err := os.MkdirAll(freshDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(freshDir, "session-2026-07-18T10-00-cccc9012.jsonl"), []byte(metaLine+firstSet+secondSet), 0o600); err != nil {
		t.Fatal(err)
	}
	freshDB := openTestDB(t)
	freshIng := New(freshDB, nil)
	freshIng.AddSource(geminiSource{root: freshRoot})
	if _, err := freshIng.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	wantRows := geminiMessageRows(t, freshDB, uuid)

	if strings.Join(gotRows, "|") != strings.Join(wantRows, "|") {
		t.Fatalf("repeated-ingest rows = %v, want (from-scratch) %v", gotRows, wantRows)
	}
}

// geminiMessageRows returns "seq:role:content" for every message of uuid, in
// seq order — used to compare a repeated-ingest DB against a from-scratch one.
func geminiMessageRows(t *testing.T, database *db.DB, uuid string) []string {
	t.Helper()
	rows, err := database.Query(`SELECT seq, role, content FROM messages WHERE session_uuid=? ORDER BY seq`, uuid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var seq int
		var role, content string
		if err := rows.Scan(&seq, &role, &content); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d:%s:%s", seq, role, content))
	}
	return out
}

// ---- Codex second-round-review fixes (findings 1-5) ----

// TestGeminiMetadataOnlySetIsNoOp is the control for finding 1: a genuine
// metadata-only $set — the "messages" key is truly ABSENT, not present-and-
// null — must remain a no-op for messages (not an abort), same as before the
// fix. This pins the boundary the present-but-null fix must not blur.
func TestGeminiMetadataOnlySetIsNoOp(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "meta-only-set", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e0000001.jsonl")
	const uuid = "e0000001-1111-4111-8111-111111111111"
	good := `{"sessionId":"` + uuid + `","startTime":"2026-07-19T10:00:00.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-19T10:00:01.000Z","type":"user","content":[{"text":"first question"}]}],"lastUpdated":"2026-07-19T10:00:01.000Z"}}` + "\n" +
		`{"$set":{"lastUpdated":"2026-07-19T10:00:02.000Z"}}` + "\n" // no "messages" key at all
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&n)
	if n != 1 {
		t.Fatalf("messages=%d want 1 (a metadata-only $set, key genuinely absent, must not change messages or abort)", n)
	}
	var unparsed int64
	database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed)
	if unparsed != 0 {
		t.Fatalf("unparsed_lines=%d want 0 (a genuine metadata-only $set is not a failure)", unparsed)
	}
}

// TestGeminiSetValueNullAborts is finding 1's first half: `{"$set": null}` —
// the "$set" key is present but its whole value is the JSON literal null.
// Without an explicit isJSONNull check on the raw $set value (before it is
// even decoded into the messages map), this would be misread as an ordinary
// metadata-only $set, wrongly leaving messages unchanged instead of
// aborting. It must abort like any other unusable $set.
func TestGeminiSetValueNullAborts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "set-value-null", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e0000002.jsonl")
	const uuid = "e0000002-2222-4222-8222-222222222222"
	good := `{"sessionId":"` + uuid + `","startTime":"2026-07-19T11:00:00.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-19T11:00:01.000Z","type":"user","content":[{"text":"question before null set"}]}],"lastUpdated":"2026-07-19T11:00:01.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	var before int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&before)
	if before != 1 {
		t.Fatalf("baseline messages=%d want 1", before)
	}
	var offsetBefore int64
	database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file=?`, path).Scan(&offsetBefore)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"$set": null}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	bumpMtime(t, path)

	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on a null $set (per-file abort only): %v", err)
	}

	var after int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&after)
	if after != before {
		t.Fatalf("messages after null $set=%d want unchanged %d (a present-but-null $set must abort, not be read as metadata-only)", after, before)
	}
	var offsetAfter int64
	database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file=?`, path).Scan(&offsetAfter)
	if offsetAfter != offsetBefore {
		t.Fatalf("last_byte_offset after null $set=%d want unchanged %d (watermark must not advance)", offsetAfter, offsetBefore)
	}
	var unparsed int64
	database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed)
	if unparsed != 1 {
		t.Fatalf("unparsed_lines after null $set=%d want 1", unparsed)
	}
}

// TestGeminiSetMessagesNullAborts is finding 1's second half:
// `{"$set":{"messages":null}}` — the "messages" key is PRESENT but its value
// is null, distinct from the metadata-only shape (key genuinely absent,
// TestGeminiMetadataOnlySetIsNoOp). Without the fix (map-based decode so the
// map's "ok" flag tells absent apart from present-null), a present-but-null
// value would be misread as "leave messages unchanged" instead of aborting,
// and — because this is a $set the caller believes carries the entire state
// — an intervening earlier $set's messages would silently remain "current" even
// though the null value is not a legitimate no-op signal. It must abort.
func TestGeminiSetMessagesNullAborts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "set-messages-null", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e0000003.jsonl")
	const uuid = "e0000003-3333-4333-8333-333333333333"
	good := `{"sessionId":"` + uuid + `","startTime":"2026-07-19T12:00:00.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-19T12:00:01.000Z","type":"user","content":[{"text":"question before null messages"}]}],"lastUpdated":"2026-07-19T12:00:01.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	var before int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&before)
	if before != 1 {
		t.Fatalf("baseline messages=%d want 1", before)
	}
	var offsetBefore int64
	database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file=?`, path).Scan(&offsetBefore)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"$set":{"messages":null}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	bumpMtime(t, path)

	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on $set.messages=null (per-file abort only): %v", err)
	}

	var after int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&after)
	if after != before {
		t.Fatalf("messages after $set.messages=null=%d want unchanged %d (present-but-null messages must abort, not be read as metadata-only)", after, before)
	}
	var offsetAfter int64
	database.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file=?`, path).Scan(&offsetAfter)
	if offsetAfter != offsetBefore {
		t.Fatalf("last_byte_offset after $set.messages=null=%d want unchanged %d", offsetAfter, offsetBefore)
	}
	var unparsed int64
	database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed)
	if unparsed != 1 {
		t.Fatalf("unparsed_lines after $set.messages=null=%d want 1", unparsed)
	}
}

// TestGeminiMessageElementNotObjectSkippedOthersIndexed is finding 2's first
// half: one element of a $set's messages[] array is not even a JSON object
// (a bare string), which fails geminiMessageEnvelope decode. This is a
// message-ELEMENT-level shape problem, not a $set-STRUCTURE problem (the
// array itself parses fine), so it must be skipped+counted, and the OTHER
// messages in the SAME $set must still be indexed normally — the whole pass
// must NOT abort.
func TestGeminiMessageElementNotObjectSkippedOthersIndexed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bad-element-shape", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e0000004.jsonl")
	const uuid = "e0000004-4444-4444-8444-444444444444"
	content := `{"sessionId":"` + uuid + `","startTime":"2026-07-19T13:00:00.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[` +
		`{"id":"msgA","timestamp":"2026-07-19T13:00:01.000Z","type":"user","content":[{"text":"good question"}]},` +
		`"not even an object",` +
		`{"id":"msgB","timestamp":"2026-07-19T13:00:02.000Z","type":"gemini","content":[{"text":"good reply"}]}` +
		`],"lastUpdated":"2026-07-19T13:00:02.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on a bad message-element shape (element-level skip only): %v", err)
	}

	var n int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&n)
	if n != 2 {
		t.Fatalf("messages=%d want 2 (the bare-string element is skipped; the other two messages in the SAME $set are still indexed)", n)
	}
	var userContent, asstContent string
	database.QueryRow(`SELECT content FROM messages WHERE session_uuid=? AND role='user'`, uuid).Scan(&userContent)
	if userContent != "good question" {
		t.Fatalf("user content=%q want %q", userContent, "good question")
	}
	database.QueryRow(`SELECT content FROM messages WHERE session_uuid=? AND role='assistant'`, uuid).Scan(&asstContent)
	if asstContent != "good reply" {
		t.Fatalf("assistant content=%q want %q", asstContent, "good reply")
	}
	var unparsed int64
	database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed)
	if unparsed < 1 {
		t.Fatalf("unparsed_lines=%d want >=1 (the bad element must be counted)", unparsed)
	}
}

// TestGeminiUnrecognizedContentShapeInSetSkippedOthersIndexed is finding 2's
// second half, and the direct P1 regression case from the second-round
// review: a "gemini"-type message whose content is a legal-JSON but
// unrecognized shape (an object, not the expected content[] array).
// Decoding straight into geminiMessage would fail (type mismatch) and, with
// the old code, aborted the ENTIRE $set — losing every other message in it.
// The fix must route this through the existing "no extractable text" skip
// path instead: warn + count unparsed, index nothing for THIS message, but
// keep every other message in the same $set.
func TestGeminiUnrecognizedContentShapeInSetSkippedOthersIndexed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bad-content-shape", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e0000005.jsonl")
	const uuid = "e0000005-5555-4555-8555-555555555555"
	content := `{"sessionId":"` + uuid + `","startTime":"2026-07-19T14:00:00.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[` +
		`{"id":"msgA","timestamp":"2026-07-19T14:00:01.000Z","type":"user","content":[{"text":"first good question"}]},` +
		`{"id":"msgB","timestamp":"2026-07-19T14:00:02.000Z","type":"gemini","content":{"text":"object shape, not an array"}},` +
		`{"id":"msgC","timestamp":"2026-07-19T14:00:03.000Z","type":"gemini","content":[{"text":"second good reply"}]}` +
		`],"lastUpdated":"2026-07-19T14:00:03.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on an unrecognized content shape (message-level skip only, not a $set abort): %v", err)
	}

	var n int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&n)
	if n != 2 {
		t.Fatalf("messages=%d want 2 (msgB's object-shaped content is skipped; msgA and msgC in the SAME $set are still indexed)", n)
	}
	var asstCount int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=? AND role='assistant'`, uuid).Scan(&asstCount)
	if asstCount != 1 {
		t.Fatalf("assistant messages=%d want 1 (only msgC; msgB's unrecognized content must not be indexed empty)", asstCount)
	}
	var asstContent string
	database.QueryRow(`SELECT content FROM messages WHERE session_uuid=? AND role='assistant'`, uuid).Scan(&asstContent)
	if asstContent != "second good reply" {
		t.Fatalf("assistant content=%q want %q", asstContent, "second good reply")
	}
	var unparsed int64
	database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed)
	if unparsed < 1 {
		t.Fatalf("unparsed_lines=%d want >=1 (msgB's unrecognized content shape must be counted)", unparsed)
	}
}

// TestGeminiRawJSONIsSetRecordLineSharedAcrossMessages is finding 3: the
// per-message raw_json must be the $set RECORD's own line (redacted), shared
// by every message that $set produces — not each message's own array
// element. This mirrors parser.go/codex.go (every message parsed off one
// line shares that line's raw_json) and is what cli/show.go's writeRaw
// consecutive-line dedup depends on to reconstruct the original file under
// `clio show --format raw` (see design.md "the per-message raw_json is the
// record's line").
func TestGeminiRawJSONIsSetRecordLineSharedAcrossMessages(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "shared-raw-json", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e0000006.jsonl")
	const uuid = "e0000006-6666-4666-8666-666666666666"
	setLine := `{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-19T15:00:01.000Z","type":"user","content":[{"text":"shared raw question"}]},{"id":"msgB","timestamp":"2026-07-19T15:00:02.000Z","type":"gemini","content":[{"text":"shared raw reply"}]}],"lastUpdated":"2026-07-19T15:00:02.000Z"}}`
	content := `{"sessionId":"` + uuid + `","startTime":"2026-07-19T15:00:00.000Z","kind":"main"}` + "\n" +
		setLine + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}

	var userRaw, asstRaw string
	database.QueryRow(`SELECT raw_json FROM messages WHERE session_uuid=? AND role='user'`, uuid).Scan(&userRaw)
	database.QueryRow(`SELECT raw_json FROM messages WHERE session_uuid=? AND role='assistant'`, uuid).Scan(&asstRaw)
	if userRaw == "" || asstRaw == "" {
		t.Fatalf("expected both messages to have non-empty raw_json, got user=%q assistant=%q", userRaw, asstRaw)
	}
	if userRaw != asstRaw {
		t.Fatalf("raw_json must be shared across every message from the SAME $set record:\n user=%s\n asst=%s", userRaw, asstRaw)
	}
	// redactJSON round-trips through encoding/json (decode -> redactWalk ->
	// re-encode), which re-serializes map keys in sorted order — so the
	// comparison target is the record line run through the same shared
	// redaction machinery the pipeline itself uses (redact.go:154), not the
	// literal original bytes.
	want := string(redactJSON([]byte(setLine)))
	if userRaw != want {
		t.Fatalf("raw_json must equal the $set record's own line, redacted:\n got =%s\n want=%s", userRaw, want)
	}
	if !strings.Contains(userRaw, "msgA") || !strings.Contains(userRaw, "msgB") {
		t.Fatalf("raw_json must contain BOTH messages' own ids (it is the whole record, not a single element): %s", userRaw)
	}
}

// TestGeminiAbortCountsPriorAccumulatedUnparsed is finding 4: a pass that
// skip-counted an ordinary unobserved-shape record (e.g. a bare
// MessageRecord) BEFORE later hitting an unusable $set that aborts the whole
// pass must not lose that earlier count. The abort's durable unparsed_lines
// bump must be 1 (the abort itself) PLUS whatever had already accumulated in
// this same pass — not a flat 1.
func TestGeminiAbortCountsPriorAccumulatedUnparsed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "prior-unparsed-then-abort", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e0000007.jsonl")
	const uuid = "e0000007-7777-4777-8777-777777777777"
	content := `{"sessionId":"` + uuid + `","startTime":"2026-07-19T16:00:00.000Z","kind":"main"}` + "\n" +
		// An unobserved shape (bare MessageRecord, no "$set" key): skip+count,
		// does NOT abort by itself. This is the "prior accumulated" skip that
		// must survive the LATER abort below.
		`{"type":"user","text":"bare append, not replayed in v1"}` + "\n" +
		// A $set whose messages value is the wrong shape (a string, not an
		// array): identified-but-unusable, aborts the whole pass.
		`{"$set":{"messages":"not an array"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on an unusable $set (per-file abort only): %v", err)
	}

	// The pass aborted, so nothing was ever committed: no session row.
	var sessN int
	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE uuid=?`, uuid).Scan(&sessN)
	if sessN != 0 {
		t.Fatalf("sessions=%d want 0 (an aborted first-ever pass must commit nothing)", sessN)
	}

	var unparsed int64
	if err := database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed); err != nil {
		t.Fatal(err)
	}
	if unparsed != 2 {
		t.Fatalf("unparsed_lines=%d want 2 (1 for the earlier bare-append skip + 1 for the abort itself; the abort must not discard the earlier count)", unparsed)
	}
}

// TestGeminiEmptySessionIDAborts is finding 5: a metadata line whose
// sessionId is present but empty must abort the pass — fail-closed, same
// semantics as a corrupted metadata/$set record — rather than commit rows
// under uuid "". Multiple broken files sharing an empty sessionId would
// otherwise collide and overwrite each other's rows under that one uuid.
func TestGeminiEmptySessionIDAborts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "empty-session-id", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e0000008.jsonl")
	content := `{"sessionId":"","startTime":"2026-07-19T17:00:00.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[{"id":"msgA","timestamp":"2026-07-19T17:00:01.000Z","type":"user","content":[{"text":"orphaned by empty sessionId"}]}],"lastUpdated":"2026-07-19T17:00:01.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on an empty sessionId (per-file abort only): %v", err)
	}

	var sessN int
	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE source_file=?`, path).Scan(&sessN)
	if sessN != 0 {
		t.Fatalf("sessions=%d want 0 (an empty sessionId must never reach commit)", sessN)
	}
	var emptyUUIDSessions int
	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE uuid=?`, "").Scan(&emptyUUIDSessions)
	if emptyUUIDSessions != 0 {
		t.Fatalf("sessions with uuid=''=%d want 0", emptyUUIDSessions)
	}
	var unparsed int64
	if err := database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed); err != nil {
		t.Fatal(err)
	}
	if unparsed != 1 {
		t.Fatalf("unparsed_lines=%d want 1 (the empty-sessionId abort must be counted)", unparsed)
	}
}

// TestGeminiSetRedactedOncePerRecordNotPerMessage is the mechanism-level half
// of finding 1 (P1, adversarial round 3): it proves the redact-and-cache path
// added to the replay loop actually runs once per $set record, not once per
// message, by inspecting parseResult directly (bypassing the DB, whose driver
// would allocate a fresh Go string per row on Scan regardless of how many
// times redact ran upstream — that round trip cannot distinguish "redacted
// once, shared" from "redacted per message" by content alone).
//
// The proof: unsafe.StringData on every message's RawJSON. Redacting the SAME
// line bytes independently, per message, would decode+redactWalk+re-encode
// each time — even with byte-for-byte identical output, encoding/json does
// not intern strings, so two independent encodes essentially never share a
// backing array. A cached, shared result reuses the exact same Go string
// value for every message, so every RawJSON's backing array address is
// identical. This directly exercises "did the fix actually skip the redo",
// which content-equality checks (already covered by
// TestGeminiRawJSONIsSetRecordLineSharedAcrossMessages) cannot tell apart
// from "recomputed but happens to match".
func TestGeminiSetRedactedOncePerRecordNotPerMessage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "redact-once-mechanism", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e000000a.jsonl")
	const uuid = "e000000a-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

	const n = 8
	var b strings.Builder
	b.WriteString(`{"$set":{"messages":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		role := "user"
		if i%2 == 1 {
			role = "gemini"
		}
		fmt.Fprintf(&b, `{"id":"msg%03d","timestamp":"2026-07-19T19:00:%02d.000Z","type":%q,"content":[{"text":"turn %d"}]}`, i, i, role, i)
	}
	b.WriteString(`],"lastUpdated":"2026-07-19T19:00:07.000Z"}}`)
	setLine := b.String()
	content := `{"sessionId":"` + uuid + `","startTime":"2026-07-19T19:00:00.000Z","kind":"main"}` + "\n" +
		setLine + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	result, err := geminiSource{root: root}.ParseFile(ing, f, 0, 0, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != n {
		t.Fatalf("messages=%d want %d", len(result.Messages), n)
	}

	want := string(redactJSON([]byte(setLine)))
	first := unsafe.StringData(result.Messages[0].RawJSON)
	for i, m := range result.Messages {
		if m.RawJSON != want {
			t.Fatalf("message[%d] raw_json mismatch:\n got =%s\n want=%s", i, m.RawJSON, want)
		}
		if unsafe.StringData(m.RawJSON) != first {
			t.Fatalf("message[%d] raw_json has a different backing array than message[0] — redact must run once per $set record with the result shared, not recomputed per message", i)
		}
	}
}

// TestGeminiSetManyMessagesRedactionSharedAndBounded is the behavioral half
// of finding 1: a $set with many messages sharing one line still (a) redacts
// correctly (a secret shape present in every message's text is scrubbed) and
// (b) ingests in bounded time — a coarse guard against an O(messages × line
// length) blow-up (a redact-per-message regression would still pass a small
// N quickly; this is not a precise benchmark, just a floor against gross
// regressions).
func TestGeminiSetManyMessagesRedactionSharedAndBounded(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "redact-once-many-messages", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e000000b.jsonl")
	const uuid = "e000000b-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

	const n = 50
	var b strings.Builder
	b.WriteString(`{"$set":{"messages":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		role := "user"
		if i%2 == 1 {
			role = "gemini"
		}
		fmt.Fprintf(&b, `{"id":"msg%03d","timestamp":"2026-07-19T20:%02d:00.000Z","type":%q,"content":[{"text":"turn %d has a token sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789AB"}]}`, i, i%60, role, i)
	}
	b.WriteString(`],"lastUpdated":"2026-07-19T20:49:00.000Z"}}`)
	setLine := b.String()
	content := `{"sessionId":"` + uuid + `","startTime":"2026-07-19T20:00:00.000Z","kind":"main"}` + "\n" +
		setLine + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})

	start := time.Now()
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("ingest of a %d-message shared-line $set took %s, want well under 5s (coarse guard against an O(messages x line length) blow-up)", n, elapsed)
	}

	rows, err := database.Query(`SELECT raw_json FROM messages WHERE session_uuid=? ORDER BY seq`, uuid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var rawJSONs []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		rawJSONs = append(rawJSONs, raw)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rawJSONs) != n {
		t.Fatalf("messages=%d want %d", len(rawJSONs), n)
	}

	want := string(redactJSON([]byte(setLine)))
	for i, raw := range rawJSONs {
		if raw != want {
			t.Fatalf("message[%d] raw_json mismatch:\n got =%s\n want=%s", i, raw, want)
		}
		if strings.Contains(raw, "sk-ant-api03-abcdefghijklmnopqrstuvwxyz") {
			t.Fatalf("message[%d] raw_json still contains the unredacted secret: %s", i, raw)
		}
	}
}

// TestGeminiNullMessageElementSkippedAndCountedOthersIndexed is finding 2
// (P2, adversarial round 3): a messages[] element that is the JSON literal
// null (not an object) must be treated like any other malformed element —
// skipped AND counted as unparsed — not silently dropped. Before the fix,
// json.Unmarshal(null, &geminiMessageEnvelope{}) succeeds as a documented
// encoding/json no-op (leaves the struct at its zero value), producing an
// envelope with Type=="" that fell through the type switch's "default"
// branch with no warning and no unparsed count — the failure was invisible.
func TestGeminiNullMessageElementSkippedAndCountedOthersIndexed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "null-element", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e000000c.jsonl")
	const uuid = "e000000c-cccc-4ccc-8ccc-cccccccccccc"
	content := `{"sessionId":"` + uuid + `","startTime":"2026-07-19T21:00:00.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[` +
		`{"id":"msgA","timestamp":"2026-07-19T21:00:01.000Z","type":"user","content":[{"text":"good question"}]},` +
		`null,` +
		`{"id":"msgB","timestamp":"2026-07-19T21:00:02.000Z","type":"gemini","content":[{"text":"good reply"}]}` +
		`],"lastUpdated":"2026-07-19T21:00:02.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on a null message element (element-level skip only): %v", err)
	}

	var n int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&n)
	if n != 2 {
		t.Fatalf("messages=%d want 2 (the null element is skipped; the other two messages in the SAME $set are still indexed)", n)
	}
	var userContent, asstContent string
	database.QueryRow(`SELECT content FROM messages WHERE session_uuid=? AND role='user'`, uuid).Scan(&userContent)
	if userContent != "good question" {
		t.Fatalf("user content=%q want %q", userContent, "good question")
	}
	database.QueryRow(`SELECT content FROM messages WHERE session_uuid=? AND role='assistant'`, uuid).Scan(&asstContent)
	if asstContent != "good reply" {
		t.Fatalf("assistant content=%q want %q", asstContent, "good reply")
	}
	var unparsed int64
	database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed)
	if unparsed < 1 {
		t.Fatalf("unparsed_lines=%d want >=1 (the null element must be counted, not silently dropped)", unparsed)
	}
}

// TestGeminiOnlyNullMessageElementCountedNoMessagesIndexed is a narrower
// variant of the null-element fix: a $set whose messages[] array contains
// ONLY a null element (no valid siblings) must index zero messages and still
// count the null as unparsed — confirming the skip+count path is taken even
// when there is nothing else in the array to "prove" the record was
// otherwise usable.
func TestGeminiOnlyNullMessageElementCountedNoMessagesIndexed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "only-null-element", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-07-19T10-00-e000000d.jsonl")
	const uuid = "e000000d-dddd-4ddd-8ddd-dddddddddddd"
	content := `{"sessionId":"` + uuid + `","startTime":"2026-07-19T22:00:00.000Z","kind":"main"}` + "\n" +
		`{"$set":{"messages":[null],"lastUpdated":"2026-07-19T22:00:00.000Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), false); err != nil {
		t.Fatalf("IngestAll must not hard-error on a messages array containing only null: %v", err)
	}

	var n int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=?`, uuid).Scan(&n)
	if n != 0 {
		t.Fatalf("messages=%d want 0 (the sole element is null, nothing to index)", n)
	}
	var unparsed int64
	database.QueryRow(`SELECT unparsed_lines FROM ingest_state WHERE source_file=?`, path).Scan(&unparsed)
	if unparsed < 1 {
		t.Fatalf("unparsed_lines=%d want >=1 (the null element must be counted even with no valid siblings)", unparsed)
	}
}

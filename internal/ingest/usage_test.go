package ingest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/db"
)

// --- helpers ---

type usageRow struct {
	Source, Model                                                   string
	Input, Output, CacheRead, CacheCreation, Reasoning, Tool, Total int64
	Categories                                                      string
}

func readUsageRows(t *testing.T, database *db.DB, uuid string) map[string]usageRow {
	t.Helper()
	rows, err := database.Query(`SELECT source, model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, tool_tokens, total_tokens, COALESCE(categories_json,'') FROM session_usage WHERE session_uuid = ?`, uuid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]usageRow{}
	for rows.Next() {
		var r usageRow
		if err := rows.Scan(&r.Source, &r.Model, &r.Input, &r.Output, &r.CacheRead, &r.CacheCreation, &r.Reasoning, &r.Tool, &r.Total, &r.Categories); err != nil {
			t.Fatal(err)
		}
		out[r.Model] = r
	}
	return out
}

func readUsageDiag(t *testing.T, database *db.DB, path string) (skipped, unmapped, stale int64) {
	t.Helper()
	if err := database.QueryRow(`SELECT usage_skipped, usage_unmapped, usage_stale FROM ingest_state WHERE source_file = ?`, path).Scan(&skipped, &unmapped, &stale); err != nil {
		t.Fatal(err)
	}
	return
}

// Claude fixture lines. uuid-a appears twice with different usage: the later
// line must win (count once). The clio-tool-only assistant event produces zero
// message rows in the text index but its usage must still be counted.
const (
	cuUser = `{"type":"user","uuid":"u-0","timestamp":"2026-07-01T10:00:00Z","sessionId":"sess-u","message":{"role":"user","content":"do the thing"}}`
	cuA1   = `{"type":"assistant","uuid":"a-1","timestamp":"2026-07-01T10:00:05Z","sessionId":"sess-u","message":{"role":"assistant","model":"model-one","content":[{"type":"text","text":"working"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}}`
	cuA1d  = `{"type":"assistant","uuid":"a-1","timestamp":"2026-07-01T10:00:05Z","sessionId":"sess-u","message":{"role":"assistant","model":"model-one","content":[{"type":"text","text":"working"}],"usage":{"input_tokens":10,"output_tokens":6,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}}`
	cuA2   = `{"type":"assistant","uuid":"a-2","timestamp":"2026-07-01T10:00:10Z","sessionId":"sess-u","message":{"role":"assistant","model":"model-two","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"mystery_tokens":7}}}`
	cuClio = `{"type":"assistant","uuid":"a-3","timestamp":"2026-07-01T10:00:15Z","sessionId":"sess-u","message":{"role":"assistant","model":"model-one","content":[{"type":"tool_use","id":"c9","name":"mcp__clio__search","input":{"query":"x"}}],"usage":{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`
	cuNoU  = `{"type":"assistant","uuid":"a-4","timestamp":"2026-07-01T10:00:20Z","sessionId":"sess-u","message":{"role":"assistant","model":"model-one","content":[{"type":"text","text":"tail"}]}}`
)

func TestClaudeUsageAggregation(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1, cuA1d, cuA2, cuClio, cuNoU)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	got := readUsageRows(t, database, "sess-u")
	m1, ok := got["model-one"]
	if !ok {
		t.Fatalf("no model-one row: %+v", got)
	}
	// a-1 counted ONCE with the LATER line's values (output 6, not 5 or 11),
	// plus the clio-tool-only event a-3 (input 1000, output 1).
	if m1.Input != 1010 || m1.Output != 7 || m1.CacheRead != 3 || m1.CacheCreation != 2 {
		t.Fatalf("model-one = %+v; want input 1010, output 7, cache_read 3, cache_creation 2", m1)
	}
	// Claude derived total: input+output+cache_read+cache_creation.
	if m1.Total != 1010+7+3+2 {
		t.Fatalf("model-one total=%d want %d", m1.Total, 1010+7+3+2)
	}
	m2, ok := got["model-two"]
	if !ok {
		t.Fatalf("no model-two row (multi-model): %+v", got)
	}
	if m2.Input != 100 || m2.Output != 50 || m2.Total != 150 {
		t.Fatalf("model-two = %+v", m2)
	}
	if m2.Categories != `{"mystery_tokens":7}` {
		t.Fatalf("model-two categories=%q want mystery_tokens preserved", m2.Categories)
	}

	// Diagnostics: exactly one should-carry-but-missing usage event (a-4); one
	// unmapped category occurrence (mystery_tokens); not stale.
	skipped, unmapped, stale := readUsageDiag(t, database, path)
	if skipped != 1 || unmapped != 1 || stale != 0 {
		t.Fatalf("diag skipped=%d unmapped=%d stale=%d; want 1,1,0", skipped, unmapped, stale)
	}
}

func TestClaudeUsageIncrementalTailRecomputesFullSession(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	// Tail-append a second assistant message; the incremental pass must yield
	// FULL-session totals (old a-1 + new a-2), not tail-only.
	appendLine(t, path, cuA2)
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	got := readUsageRows(t, database, "sess-u")
	if got["model-one"].Input != 10 || got["model-two"].Input != 100 {
		t.Fatalf("full-session recompute missing rows: %+v", got)
	}

	// Idempotency: force a full re-ingest; aggregates must be identical.
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	again := readUsageRows(t, database, "sess-u")
	if len(again) != len(got) || again["model-one"] != got["model-one"] || again["model-two"] != got["model-two"] {
		t.Fatalf("re-ingest changed aggregates: %+v vs %+v", again, got)
	}
}

func TestClaudeUsageCountersReplaceAcrossAppends(t *testing.T) {
	projects := t.TempDir()
	// One malformed should-carry event (assistant without usage).
	path := writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuNoU)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	// Three successive appends, each triggering a whole-file usage scan: the
	// malformed line must stay counted ONCE (replace semantics), never 3x.
	for _, line := range []string{cuA1, cuA2, cuUser} {
		appendLine(t, path, line)
		if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
			t.Fatal(err)
		}
	}
	skipped, _, _ := readUsageDiag(t, database, path)
	if skipped != 1 {
		t.Fatalf("usage_skipped=%d want 1 (whole-file pass must replace, not accumulate)", skipped)
	}
}

func TestUsageScanFailedIsAtomicNoopFlaggedStale(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	before := readUsageRows(t, database, "sess-u")
	if len(before) == 0 {
		t.Fatal("expected seeded usage rows")
	}

	// Simulate a failed scan on a full rebuild via commit(): outcome
	// usageScanFailed must neither replace nor delete, and must raise the flag.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := claudeCodeSource{}.ParseFile(ing, mustOpen(t, path), 0, 0, path)
	if err != nil {
		t.Fatal(err)
	}
	failed := &usageResult{Outcome: usageScanFailed}
	fst := FileState{
		SourceFile: path, LastSize: fi.Size(), LastMTime: fi.ModTime().UnixNano(),
		LastByteOffset: res.Consumed, UsageStale: 1, UsagePolicy: usageCountersPreserve,
	}
	if _, err := ing.commit(changeFull, res.Session, res.Messages, res.ClioIDs, failed, fst); err != nil {
		t.Fatal(err)
	}
	after := readUsageRows(t, database, "sess-u")
	if len(after) != len(before) || after["model-one"] != before["model-one"] {
		t.Fatalf("scan-failed commit changed usage rows: %+v vs %+v", after, before)
	}
	_, _, stale := readUsageDiag(t, database, path)
	if stale != 1 {
		t.Fatalf("usage_stale=%d want 1", stale)
	}

	// A successful rescan clears the flag.
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	_, _, stale = readUsageDiag(t, database, path)
	if stale != 0 {
		t.Fatalf("usage_stale=%d want 0 after successful rescan", stale)
	}
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestCrossSourceConflictLeavesUsageUntouched(t *testing.T) {
	database := openTestDB(t)
	ing := New(database, nil)
	// Existing codex-owned session with usage rows.
	if _, err := database.Exec(`INSERT INTO sessions(uuid, source_file, turn_count, source) VALUES ('dup-9','/codex/dup-9.jsonl',0,'codex')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO session_usage(session_uuid, source, model, input_tokens, total_tokens) VALUES ('dup-9','codex','(mixed)',42,42)`); err != nil {
		t.Fatal(err)
	}
	// A Claude file colliding on the same uuid, carrying usage of its own.
	projects := t.TempDir()
	claudeLine := `{"type":"assistant","uuid":"x-1","sessionId":"dup-9","timestamp":"2026-07-01T10:00:00Z","message":{"role":"assistant","model":"m","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":9,"output_tokens":9}}}`
	path := writeSession(t, projects, "-Users-x-proj", "dup-9", claudeLine)
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	got := readUsageRows(t, database, "dup-9")
	if len(got) != 1 {
		t.Fatalf("usage rows=%d want 1 (conflict path must be a usage no-op)", len(got))
	}
	if r := got["(mixed)"]; r.Source != "codex" || r.Input != 42 {
		t.Fatalf("existing codex usage row polluted: %+v", r)
	}
}

// --- Codex ---

func writeUsageRollout(t *testing.T, root, uuid string, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "rollout-2026-07-01T10-00-00-"+uuid+".jsonl")
	var data []byte
	for _, l := range lines {
		data = append(data, []byte(l+"\n")...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const codexUsageUUID = "0199cccc-dddd-7eee-8fff-222222222222"

const (
	cxMeta = `{"timestamp":"2026-07-01T10:00:00Z","type":"session_meta","payload":{"id":"` + codexUsageUUID + `","cwd":"/Users/dev/p"}}`
	cxUser = `{"timestamp":"2026-07-01T10:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`
	cxTok1 = `{"timestamp":"2026-07-01T10:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10,"reasoning_output_tokens":5,"total_tokens":110}},"rate_limits":{"limit_id":"codex","plan_type":"plus","primary":{"used_percent":10.0,"window_minutes":10080,"resets_at":1785000000}}}}`
	cxTok2 = `{"timestamp":"2026-07-01T10:00:09Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300,"cached_input_tokens":120,"output_tokens":33,"reasoning_output_tokens":8,"total_tokens":333}},"rate_limits":{"limit_id":"codex","plan_type":"plus","primary":{"used_percent":11.5,"window_minutes":10080,"resets_at":1785000000}}}}`
	cxAsst = `{"timestamp":"2026-07-01T10:00:10Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`
)

func TestCodexUsageLatestCumulativeWins(t *testing.T) {
	root := t.TempDir()
	path := writeUsageRollout(t, root, codexUsageUUID, cxMeta, cxUser, cxTok1, cxTok2, cxAsst)
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(codexSource{root: root})
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	got := readUsageRows(t, database, codexUsageUUID)
	r, ok := got["(mixed)"]
	if !ok || len(got) != 1 {
		t.Fatalf("want exactly one (mixed) row, got %+v", got)
	}
	// Latest cumulative value, NOT the sum of both events.
	if r.Input != 300 || r.CacheRead != 120 || r.Output != 33 || r.Reasoning != 8 || r.Total != 333 {
		t.Fatalf("codex row=%+v want latest cumulative (300/120/33/8/333)", r)
	}
	if r.Source != "codex" {
		t.Fatalf("source=%q", r.Source)
	}

	// Quota snapshot captured with the LATER observation.
	var usedPct float64
	var observed int64
	var plan string
	if err := database.QueryRow(`SELECT used_percent, observed_at, plan_type FROM quota_snapshots WHERE source='codex' AND limit_id='codex:primary'`).Scan(&usedPct, &observed, &plan); err != nil {
		t.Fatal(err)
	}
	if usedPct != 11.5 || plan != "plus" {
		t.Fatalf("snapshot used_percent=%v plan=%q; want 11.5/plus", usedPct, plan)
	}

	// A tail with no token events is a usage no-op.
	appendLine(t, path, cxAsst)
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	after := readUsageRows(t, database, codexUsageUUID)
	if after["(mixed)"] != r {
		t.Fatalf("tail without token events changed usage: %+v vs %+v", after["(mixed)"], r)
	}
}

func TestCodexFullRebuildWithoutUsageDeletesStaleRow(t *testing.T) {
	root := t.TempDir()
	path := writeUsageRollout(t, root, codexUsageUUID, cxMeta, cxUser, cxTok1, cxAsst)
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(codexSource{root: root})
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	if got := readUsageRows(t, database, codexUsageUUID); len(got) != 1 {
		t.Fatalf("seed failed: %+v", got)
	}
	// Rewrite the file WITHOUT any token events; the full rebuild must delete
	// the stale usage row.
	writeUsageRollout(t, root, codexUsageUUID, cxMeta, cxUser, cxAsst)
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	if got := readUsageRows(t, database, codexUsageUUID); len(got) != 0 {
		t.Fatalf("stale usage row not deleted: %+v", got)
	}
}

func TestQuotaSnapshotOlderObservationNeverOverwritesNewer(t *testing.T) {
	root := t.TempDir()
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(codexSource{root: root})
	// Newer file first.
	newer := writeUsageRollout(t, root, codexUsageUUID, cxMeta, cxUser, cxTok2, cxAsst)
	if _, _, err := ing.IngestFile(context.Background(), newer, false); err != nil {
		t.Fatal(err)
	}
	// Older rollout (different session, earlier observed_at) processed after.
	olderUUID := "0199dddd-eeee-7fff-8aaa-333333333333"
	older := writeUsageRollout(t, root, olderUUID,
		`{"timestamp":"2026-06-01T10:00:00Z","type":"session_meta","payload":{"id":"`+olderUUID+`","cwd":"/Users/dev/p"}}`,
		`{"timestamp":"2026-06-01T10:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1,"total_tokens":1}},"rate_limits":{"limit_id":"codex","plan_type":"plus","primary":{"used_percent":99.0,"window_minutes":10080,"resets_at":1780000000}}}}`)
	if _, _, err := ing.IngestFile(context.Background(), older, false); err != nil {
		t.Fatal(err)
	}
	var usedPct float64
	if err := database.QueryRow(`SELECT used_percent FROM quota_snapshots WHERE source='codex' AND limit_id='codex:primary'`).Scan(&usedPct); err != nil {
		t.Fatal(err)
	}
	if usedPct != 11.5 {
		t.Fatalf("older observation overwrote newer snapshot: used_percent=%v want 11.5", usedPct)
	}
}

// --- Gemini ---

func writeGeminiChat(t *testing.T, root, hash, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, hash, "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	var data []byte
	for _, l := range lines {
		data = append(data, []byte(l+"\n")...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGeminiUsageSummedFromReplay(t *testing.T) {
	root := t.TempDir()
	meta := `{"sessionId":"gem-u1","projectHash":"h1","startTime":"2026-07-01T10:00:00Z","lastUpdated":"2026-07-01T10:01:00Z","kind":"chat"}`
	set := `{"$set":{"messages":[` +
		`{"id":"m1","timestamp":"2026-07-01T10:00:01Z","type":"user","content":[{"text":"hi"}]},` +
		`{"id":"m2","timestamp":"2026-07-01T10:00:02Z","type":"gemini","model":"gemini-x","content":[{"text":"hello"}],"tokens":{"input":100,"output":20,"cached":5,"thoughts":30,"tool":0,"total":155}},` +
		`{"id":"m3","timestamp":"2026-07-01T10:00:03Z","type":"gemini","model":"gemini-x","content":[],"tokens":{"input":50,"output":10,"cached":0,"thoughts":5,"tool":2,"total":67}}` +
		`]}}`
	path := writeGeminiChat(t, root, "h1", "session-2026-07-01T10-00-aaaa1111.jsonl", meta, set)
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	got := readUsageRows(t, database, "gem-u1")
	r, ok := got["gemini-x"]
	if !ok || len(got) != 1 {
		t.Fatalf("want one gemini-x row, got %+v", got)
	}
	// m3 has no extractable text (skipped by the text pass) but its tokens MUST
	// still count: sums are 150/30/5/35/2, native total 222.
	if r.Input != 150 || r.Output != 30 || r.CacheRead != 5 || r.Reasoning != 35 || r.Tool != 2 || r.Total != 222 {
		t.Fatalf("gemini row=%+v want 150/30/5/35/2 total 222", r)
	}

	// Replay idempotency: re-ingest the same file fully; identical aggregate.
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	again := readUsageRows(t, database, "gem-u1")
	if again["gemini-x"] != r {
		t.Fatalf("replay not idempotent: %+v vs %+v", again["gemini-x"], r)
	}
}

func TestOversizedLineSkippedUsageScanContinues(t *testing.T) {
	projects := t.TempDir()
	// A historical over-cap line must be skipped+counted, and later appends
	// must still update the aggregate (never permanently blocked).
	big := `{"type":"user","sessionId":"sess-u","message":{"role":"user","content":"` + string(make([]byte, 0)) + bigFiller() + `"}}`
	path := writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, big, cuA1)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	got := readUsageRows(t, database, "sess-u")
	if got["model-one"].Input != 10 {
		t.Fatalf("usage not extracted past oversized line: %+v", got)
	}
	skipped, _, stale := readUsageDiag(t, database, path)
	if skipped != 1 || stale != 0 {
		t.Fatalf("skipped=%d stale=%d; want 1,0 (oversized counts, never fatal)", skipped, stale)
	}
	// Later append still updates.
	appendLine(t, path, cuA2)
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	if got := readUsageRows(t, database, "sess-u"); got["model-two"].Input != 100 {
		t.Fatalf("append after oversized line did not update usage: %+v", got)
	}
}

// bigFiller returns a filler string pushing a line over maxLineBytes.
func bigFiller() string {
	b := make([]byte, maxLineBytes+1024)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func TestCodexIncrementalUsageCountersAccumulate(t *testing.T) {
	root := t.TempDir()
	// A malformed token_count (no info) in the initial full ingest: skipped=1.
	badTok := `{"timestamp":"2026-07-01T10:00:03Z","type":"event_msg","payload":{"type":"token_count"}}`
	path := writeUsageRollout(t, root, codexUsageUUID, cxMeta, cxUser, badTok, cxAsst)
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(codexSource{root: root})
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	skipped, _, _ := readUsageDiag(t, database, path)
	if skipped != 1 {
		t.Fatalf("skipped=%d want 1 after full ingest", skipped)
	}
	// A second malformed token_count arrives as an incremental tail: Codex is
	// tail-scoped extraction, so counters ACCUMULATE (1+1=2).
	appendLine(t, path, badTok)
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	skipped, _, _ = readUsageDiag(t, database, path)
	if skipped != 2 {
		t.Fatalf("skipped=%d want 2 (codex incremental accumulates)", skipped)
	}
}

// BenchmarkCommitWithUsage / BenchmarkCommitWithoutUsage measure the
// write-transaction (lock-hold) cost of the usage replacement in the
// spec-mandated shape: a 5,000-message session receiving ONE incremental tail
// commit (a single appended message), with vs without the usage replacement.
// The usage scan itself runs OUTSIDE the transaction, so the in-tx delta is
// just DELETE + a handful of INSERTs. Gate: < 10% change.
func benchTailCommitLong(b *testing.B, withUsage bool) {
	projects := b.TempDir()
	lines := make([]string, 0, 5000)
	for i := 0; i < 5000; i++ {
		if i%2 == 0 {
			lines = append(lines, fmt.Sprintf(`{"type":"user","uuid":"u-%d","timestamp":"2026-07-01T10:00:00Z","sessionId":"bench-s","message":{"role":"user","content":"turn %d lorem ipsum dolor"}}`, i, i))
		} else {
			lines = append(lines, fmt.Sprintf(`{"type":"assistant","uuid":"a-%d","timestamp":"2026-07-01T10:00:01Z","sessionId":"bench-s","message":{"role":"assistant","model":"m","content":[{"type":"text","text":"reply %d xxxxxxxxxxxxxxxxxxxxxxxx"}],"usage":{"input_tokens":10,"output_tokens":5}}}`, i, i))
		}
	}
	dir := filepath.Join(projects, "-Users-x-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(dir, "bench-s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	database, err := db.Open(filepath.Join(b.TempDir(), "bench.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	ing := New(database, nil)
	// Index the whole session once (setup, not measured).
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		b.Fatal(err)
	}
	// Each iteration is a REAL fresh tail: append a unique line, parse the new
	// incremental state (untimed), then time ONLY the commit (the write
	// transaction). No iteration re-commits a duplicate.
	prior, err := ing.loadState(path)
	if err != nil || prior == nil {
		b.Fatal("no prior state")
	}
	offset := prior.LastByteOffset
	seq := 5000
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		tail := fmt.Sprintf(`{"type":"assistant","uuid":"a-tail-%d","timestamp":"2026-07-01T11:00:00Z","sessionId":"bench-s","message":{"role":"assistant","model":"m","content":[{"type":"text","text":"tail %d"}],"usage":{"input_tokens":1,"output_tokens":1}}}`, i, i) + "\n"
		af, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := af.WriteString(tail); err != nil {
			b.Fatal(err)
		}
		af.Close()
		f, err := os.Open(path)
		if err != nil {
			b.Fatal(err)
		}
		res, perr := claudeCodeSource{}.ParseFile(ing, f, offset, seq, path)
		f.Close()
		if perr != nil {
			b.Fatal(perr)
		}
		fi, _ := os.Stat(path)
		usage := res.Usage
		if !withUsage {
			usage = nil
		}
		fst := FileState{SourceFile: path, LastSize: fi.Size(), LastMTime: fi.ModTime().UnixNano(),
			LastByteOffset: offset + res.Consumed, UsagePolicy: usageCountersReplace}
		b.StartTimer()
		if _, err := ing.commit(changeIncremental, res.Session, res.Messages, res.ClioIDs, usage, fst); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		offset += res.Consumed
		seq++
		b.StartTimer()
	}
}

func BenchmarkCommitWithUsage(b *testing.B)    { benchTailCommitLong(b, true) }
func BenchmarkCommitWithoutUsage(b *testing.B) { benchTailCommitLong(b, false) }

func TestClaudeUsageTombstoneLastWins(t *testing.T) {
	projects := t.TempDir()
	// Full scan: a later same-uuid line WITHOUT usage tombstones the earlier one.
	tomb := `{"type":"assistant","uuid":"a-1","timestamp":"2026-07-01T10:00:06Z","sessionId":"sess-u","message":{"role":"assistant","model":"model-one","content":[{"type":"text","text":"rewrite"}]}}`
	writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1, tomb, cuA2)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	got := readUsageRows(t, database, "sess-u")
	if _, has := got["model-one"]; has {
		t.Fatalf("tombstoned a-1 usage still present: %+v", got)
	}
	if got["model-two"].Input != 100 {
		t.Fatalf("unrelated row lost: %+v", got)
	}

	// Incremental tail: appending a usage-less line for an existing uuid must
	// also clear it (this is the exact counterexample that killed the tail
	// shortcut).
	path2 := writeSession(t, projects, "-Users-x-proj", "sess-v",
		`{"type":"user","uuid":"u-0","timestamp":"2026-07-01T10:00:00Z","sessionId":"sess-v","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","uuid":"b-1","timestamp":"2026-07-01T10:00:05Z","sessionId":"sess-v","message":{"role":"assistant","model":"model-one","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":7,"output_tokens":3}}}`)
	if _, _, err := ing.IngestFile(context.Background(), path2, false); err != nil {
		t.Fatal(err)
	}
	if readUsageRows(t, database, "sess-v")["model-one"].Input != 7 {
		t.Fatal("seed failed")
	}
	appendLine(t, path2, `{"type":"assistant","uuid":"b-1","timestamp":"2026-07-01T10:00:09Z","sessionId":"sess-v","message":{"role":"assistant","model":"model-one","content":[{"type":"text","text":"revised"}]}}`)
	if _, _, err := ing.IngestFile(context.Background(), path2, false); err != nil {
		t.Fatal(err)
	}
	if got := readUsageRows(t, database, "sess-v"); len(got) != 0 {
		t.Fatalf("incremental tombstone did not clear usage: %+v", got)
	}
}

func TestPurgeMissingDeletesUsage(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	if len(readUsageRows(t, database, "sess-u")) == 0 {
		t.Fatal("seed failed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := ing.PurgeMissing(context.Background(), projects); err != nil {
		t.Fatal(err)
	}
	if got := readUsageRows(t, database, "sess-u"); len(got) != 0 {
		t.Fatalf("purge left usage rows: %+v", got)
	}
}

func TestClaudeUsageIgnoresUncommittedTailLine(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1)
	// Append a PARTIAL line (no trailing newline) carrying big usage: beyond
	// the committed watermark, must not count.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	partial := `{"type":"assistant","uuid":"a-9","timestamp":"2026-07-01T10:01:00Z","sessionId":"sess-u","message":{"role":"assistant","model":"model-one","content":[{"type":"text","text":"partial"}],"usage":{"input_tokens":999999,"output_tokens":1}}}`
	if _, err := f.WriteString(partial); err != nil {
		t.Fatal(err)
	}
	f.Close()
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	if got := readUsageRows(t, database, "sess-u"); got["model-one"].Input != 10 {
		t.Fatalf("uncommitted tail counted: %+v", got)
	}
	// Complete the line: now it counts.
	appendLine(t, path, "")
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	if got := readUsageRows(t, database, "sess-u"); got["model-one"].Input != 10+999999 {
		t.Fatalf("completed line not counted: %+v", got)
	}
}

func TestGeminiReplayWithoutUsageDeletesStaleRow(t *testing.T) {
	root := t.TempDir()
	meta := `{"sessionId":"gem-u2","projectHash":"h2","startTime":"2026-07-01T10:00:00Z","lastUpdated":"2026-07-01T10:01:00Z","kind":"chat"}`
	withTok := `{"$set":{"messages":[{"id":"m1","timestamp":"2026-07-01T10:00:02Z","type":"gemini","model":"gemini-x","content":[{"text":"hi"}],"tokens":{"input":10,"output":2,"total":12}}]}}`
	path := writeGeminiChat(t, root, "h2", "session-2026-07-01T10-00-bbbb2222.jsonl", meta, withTok)
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	if len(readUsageRows(t, database, "gem-u2")) != 1 {
		t.Fatal("seed failed")
	}
	// Rewrite: replay yields ONLY a user message (no usage) -> stale row deleted.
	writeGeminiChat(t, root, "h2", "session-2026-07-01T10-00-bbbb2222.jsonl", meta,
		`{"$set":{"messages":[{"id":"m1","timestamp":"2026-07-01T10:00:02Z","type":"user","content":[{"text":"only user"}]}]}}`)
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	if got := readUsageRows(t, database, "gem-u2"); len(got) != 0 {
		t.Fatalf("stale gemini usage row not deleted: %+v", got)
	}
}

func TestFullReindexBackfillsPreFeatureDB(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-feature DB: text rows exist, usage rows absent.
	if _, err := database.Exec(`DELETE FROM session_usage`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	if got := readUsageRows(t, database, "sess-u"); got["model-one"].Input != 10 {
		t.Fatalf("full reindex did not backfill usage: %+v", got)
	}
}

func TestScanClaudeUsageHardErrorIsScanFailed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	if err := os.WriteFile(path, []byte(cuA1+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close() // closed file: Seek/Read fails -> hard error, not "no usage"
	res := scanClaudeUsage(f, 100, path, testLogger())
	if res.Outcome != usageScanFailed {
		t.Fatalf("outcome=%v want usageScanFailed on hard read error", res.Outcome)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCodexTokenCountWithoutNativeTotalRejected(t *testing.T) {
	root := t.TempDir()
	// Second token_count lacks total_tokens: must be rejected + counted, and
	// the FIRST (valid) observation kept.
	noTotal := `{"timestamp":"2026-07-01T10:00:05Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":9999,"output_tokens":9999}}}}`
	path := writeUsageRollout(t, root, codexUsageUUID, cxMeta, cxUser, cxTok1, noTotal, cxAsst)
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(codexSource{root: root})
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	got := readUsageRows(t, database, codexUsageUUID)
	if got["(mixed)"].Total != 110 {
		t.Fatalf("rejected record overwrote valid observation: %+v", got)
	}
	skipped, _, _ := readUsageDiag(t, database, path)
	if skipped != 1 {
		t.Fatalf("skipped=%d want 1 (no-native-total token_count counted)", skipped)
	}
}

func TestGeminiUsageMissingTokensCounted(t *testing.T) {
	root := t.TempDir()
	meta := `{"sessionId":"gem-u3","projectHash":"h3","startTime":"2026-07-01T10:00:00Z","lastUpdated":"2026-07-01T10:01:00Z","kind":"chat"}`
	set := `{"$set":{"messages":[` +
		`{"id":"m1","timestamp":"2026-07-01T10:00:01Z","type":"gemini","model":"g","content":[{"text":"no tokens at all"}]},` +
		`{"id":"m2","timestamp":"2026-07-01T10:00:02Z","type":"gemini","model":"g","content":[{"text":"no native total"}],"tokens":{"input":5,"output":5}},` +
		`{"id":"m3","timestamp":"2026-07-01T10:00:03Z","type":"gemini","model":"g","content":[{"text":"ok"}],"tokens":{"input":1,"output":1,"total":2}}` +
		`]}}`
	path := writeGeminiChat(t, root, "h3", "session-2026-07-01T10-00-cccc3333.jsonl", meta, set)
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(geminiSource{root: root})
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	// Only m3 counts; m1 (assistant without tokens) and m2 (no native total)
	// are each counted as skipped, never persisted as partial data.
	got := readUsageRows(t, database, "gem-u3")
	if got["g"].Total != 2 || got["g"].Input != 1 {
		t.Fatalf("partial/absent tokens leaked into aggregate: %+v", got)
	}
	skipped, _, _ := readUsageDiag(t, database, path)
	if skipped != 2 {
		t.Fatalf("skipped=%d want 2", skipped)
	}
}

func TestClaudeUsageMissingUUIDCountedNeverSummed(t *testing.T) {
	projects := t.TempDir()
	noUUID := `{"type":"assistant","timestamp":"2026-07-01T10:00:07Z","sessionId":"sess-u","message":{"role":"assistant","model":"model-one","content":[{"type":"text","text":"anon"}],"usage":{"input_tokens":77777,"output_tokens":1}}}`
	path := writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1, noUUID)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	got := readUsageRows(t, database, "sess-u")
	if got["model-one"].Input != 10 {
		t.Fatalf("missing-uuid usage was summed: %+v", got)
	}
	skipped, _, _ := readUsageDiag(t, database, path)
	if skipped != 1 {
		t.Fatalf("skipped=%d want 1 (missing uuid counted as malformed)", skipped)
	}
}

func TestCodexUndecodableTokenCountPayloadCounted(t *testing.T) {
	root := t.TempDir()
	// payload is not an object but mentions token_count: identifiably a broken
	// token_count -> counted; an unrelated broken payload -> not counted.
	brokenTC := `{"timestamp":"2026-07-01T10:00:03Z","type":"event_msg","payload":["token_count",42]}`
	brokenOther := `{"timestamp":"2026-07-01T10:00:04Z","type":"event_msg","payload":["something_else"]}`
	path := writeUsageRollout(t, root, codexUsageUUID, cxMeta, cxUser, brokenTC, brokenOther, cxTok1, cxAsst)
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(codexSource{root: root})
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	if got := readUsageRows(t, database, codexUsageUUID); got["(mixed)"].Total != 110 {
		t.Fatalf("valid token_count lost: %+v", got)
	}
	skipped, _, _ := readUsageDiag(t, database, path)
	if skipped != 1 {
		t.Fatalf("skipped=%d want 1 (broken token_count counted, unrelated payload not)", skipped)
	}
}

func TestClaudeUsageCountersFullReingestReplaces(t *testing.T) {
	projects := t.TempDir()
	// Two malformed should-carry events -> skipped=2.
	noU2 := `{"type":"assistant","uuid":"a-5","timestamp":"2026-07-01T10:00:21Z","sessionId":"sess-u","message":{"role":"assistant","model":"model-one","content":[{"type":"text","text":"tail2"}]}}`
	path := writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1, cuNoU, noU2)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	if skipped, _, _ := readUsageDiag(t, database, path); skipped != 2 {
		t.Fatalf("seed skipped=%d want 2", skipped)
	}
	// Rewrite the file with only ONE malformed event: the full re-ingest must
	// REPLACE the counter with this run's count (1), never accumulate to 3.
	writeSession(t, projects, "-Users-x-proj", "sess-u", cuUser, cuA1, cuNoU)
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	if skipped, _, _ := readUsageDiag(t, database, path); skipped != 1 {
		t.Fatalf("skipped=%d want 1 after full re-ingest (replace semantics)", skipped)
	}
}

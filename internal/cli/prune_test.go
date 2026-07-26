package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
	"github.com/linhn0617/clio/internal/sessions"
)

// pruneSandbox sets HOME/XDG to temp dirs, writes a Claude session file under
// the real projects layout, indexes it, and returns the opened DB + the source
// file path. The session is dated old (ended_at in the past) via a direct update.
func pruneSandbox(t *testing.T, uuid string, ageDays int) (*db.DB, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	projects := filepath.Join(home, ".claude", "projects", "-Users-x-proj")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projects, uuid+".jsonl")
	line := `{"type":"assistant","uuid":"a1","timestamp":"2026-01-01T10:00:00Z","sessionId":"` + uuid + `","message":{"role":"assistant","model":"m","content":[{"type":"text","text":"hello world"}]}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(home, "xdg", "clio", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ing := ingest.New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	// Age the session so it falls before an --older-than cutoff.
	old := time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour).Unix()
	if _, err := database.Exec(`UPDATE sessions SET ended_at=?, started_at=? WHERE uuid=?`, old, old, uuid); err != nil {
		t.Fatal(err)
	}
	return database, path
}

func prunePairs(t *testing.T, d *db.DB, uuids ...string) []sessions.PrunePair {
	t.Helper()
	var out []sessions.PrunePair
	for _, u := range uuids {
		var sf string
		if err := d.QueryRow(`SELECT source_file FROM sessions WHERE uuid=?`, u).Scan(&sf); err != nil {
			t.Fatal(err)
		}
		out = append(out, sessions.PrunePair{UUID: u, SourceFile: sf})
	}
	return out
}

func rawJSONOf(t *testing.T, d *db.DB, uuid string) []string {
	t.Helper()
	rows, err := d.Query(`SELECT raw_json FROM messages WHERE session_uuid=? ORDER BY seq`, uuid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		rows.Scan(&r)
		out = append(out, r)
	}
	return out
}

func TestPruneRawBlanksOldRestorable(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	before := rawJSONOf(t, database, "sess-old")
	if len(before) == 0 || before[0] == "" {
		t.Fatal("expected non-empty raw_json before prune")
	}
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	cands, err := sessions.PruneCandidates(context.Background(), database, cutoff, "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("candidates=%d want 1", len(cands))
	}
	pr, err := sessions.PruneRawJSON(context.Background(), database, cutoff, "all", "", prunePairs(t, database, "sess-old"), false)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Messages != 1 || pr.RemovedBytes == 0 {
		t.Fatalf("pruned msgs=%d removed=%d want 1 / >0", pr.Messages, pr.RemovedBytes)
	}
	after := rawJSONOf(t, database, "sess-old")
	if after[0] != "" {
		t.Fatalf("raw_json not blanked: %q", after[0])
	}
	// Content + FTS intact.
	var contentCnt, ftsHit int64
	database.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid=? AND content<>''`, "sess-old").Scan(&contentCnt)
	database.QueryRow(`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'hello'`).Scan(&ftsHit)
	if contentCnt == 0 || ftsHit == 0 {
		t.Fatalf("content/FTS damaged: content=%d ftsHit=%d", contentCnt, ftsHit)
	}
}

func TestPruneRawIdempotentRerun(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	if m, _, _ := mustPrune(t, database, cutoff, "sess-old"); m != 1 {
		t.Fatalf("first prune msgs=%d want 1", m)
	}
	if m, _, _ := mustPrune(t, database, cutoff, "sess-old"); m != 0 {
		t.Fatalf("rerun pruned %d want 0 (idempotent)", m)
	}
}

func mustPrune(t *testing.T, d *db.DB, cutoff int64, uuid string) (int64, int64, error) {
	t.Helper()
	pr, err := sessions.PruneRawJSON(context.Background(), d, cutoff, "all", "", prunePairs(t, d, uuid), false)
	return pr.Messages, pr.RemovedBytes, err
}

func TestPruneRawSkipsUnverifiedAborted(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	// Mark the ingest snapshot unverified (aborted=1) — restorability unknown.
	if _, err := database.Exec(`UPDATE ingest_state SET aborted=1 WHERE source_file=?`, path); err != nil {
		t.Fatal(err)
	}
	// The revalidation inside PruneRawJSON must refuse it even if passed as eligible.
	pr, err := sessions.PruneRawJSON(context.Background(), database, time.Now().Add(-14*24*time.Hour).Unix(), "all", "", prunePairs(t, database, "sess-old"), false)
	if err != nil {
		t.Fatal(err)
	}
	if pr.PrunedSessions != 0 {
		t.Fatalf("aborted session pruned %d want 0", pr.PrunedSessions)
	}
	if rawJSONOf(t, database, "sess-old")[0] == "" {
		t.Fatal("aborted session raw_json was blanked")
	}
}

func TestPruneRawRecentNotEligible(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-recent", 1) // 1 day old
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	cands, _ := sessions.PruneCandidates(context.Background(), database, cutoff, "all", "")
	if len(cands) != 0 {
		t.Fatalf("recent session should not be a candidate, got %d", len(cands))
	}
}

func TestPruneRawCommandDryRunAndVacuum(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	database.Close() // command opens its own handle

	// --dry-run: reports, writes nothing.
	out := runPruneCmd(t, "--older-than", "14d", "--dry-run", "--source", "all")
	if !strings.Contains(out, "would prune") {
		t.Fatalf("dry-run output missing 'would prune':\n%s", out)
	}
	d2 := reopen(t)
	if rawJSONOf(t, d2, "sess-old")[0] == "" {
		t.Fatal("dry-run blanked raw_json (must not write)")
	}
	d2.Close()

	// Real prune with --vacuum.
	out = runPruneCmd(t, "--older-than", "14d", "--vacuum", "--source", "all")
	if !strings.Contains(out, "pruned 1 messages") || !strings.Contains(out, "VACUUM done") {
		t.Fatalf("prune --vacuum output unexpected:\n%s", out)
	}
	d3 := reopen(t)
	if rawJSONOf(t, d3, "sess-old")[0] != "" {
		t.Fatal("prune did not blank raw_json")
	}
	d3.Close()
}

func TestPruneRawRequiresDuration(t *testing.T) {
	pruneSandbox(t, "sess-old", 30)
	cmd := newPruneRawCmd()
	cmd.SetArgs([]string{"--source", "all"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("missing --older-than should error")
	}
}

func reopen(t *testing.T) *db.DB {
	t.Helper()
	dbPath := filepath.Join(os.Getenv("XDG_DATA_HOME"), "clio", "db.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func runPruneCmd(t *testing.T, args ...string) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	cmd := newPruneRawCmd()
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	if err != nil {
		t.Fatalf("prune command failed: %v\n%s", err, buf.String())
	}
	return buf.String()
}

func TestPruneRestoreRoundTrip(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	if m, _, _ := mustPrune(t, database, cutoff, "sess-old"); m != 1 {
		t.Fatal("prune failed to seed")
	}
	if rawJSONOf(t, database, "sess-old")[0] != "" {
		t.Fatal("not pruned")
	}
	// Residual check sees the pruned session.
	res, err := sessions.PrunedResiduals(context.Background(), database)
	if err != nil || len(res) != 1 || res[0] != "sess-old" {
		t.Fatalf("residuals=%v err=%v want [sess-old]", res, err)
	}
	database.Close()

	// A full reindex restores raw_json from the still-present source file.
	_ = path
	out := runIndexFull(t)
	if strings.Contains(out, "could not be restored") {
		t.Fatalf("index --full reported unrestored prune:\n%s", out)
	}
	d2 := reopen(t)
	defer d2.Close()
	if rawJSONOf(t, d2, "sess-old")[0] == "" {
		t.Fatal("index --full did not restore raw_json")
	}
	if res, _ := sessions.PrunedResiduals(context.Background(), d2); len(res) != 0 {
		t.Fatalf("residuals after restore=%v want empty", res)
	}
}

func TestPruneRestoreFailureSurfaced(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	mustPrune(t, database, cutoff, "sess-old")
	database.Close()
	// Remove the source file: full reindex cannot restore → non-zero + surfaced.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cmd := newIndexCmd()
	cmd.SetArgs([]string{"--full"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	// index --full purges missing files, so the pruned session's rows may be
	// removed entirely (no residual). Either outcome is acceptable: no silent
	// pruned-but-present row remains. Assert the residual set is empty OR the
	// command exited non-zero.
	err := cmd.Execute()
	d2 := reopen(t)
	defer d2.Close()
	res, _ := sessions.PrunedResiduals(context.Background(), d2)
	if err == nil && len(res) > 0 {
		t.Fatalf("pruned session with missing file left as silent residual: %v", res)
	}
}

func runIndexFull(t *testing.T) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	cmd := newIndexCmd()
	cmd.SetArgs([]string{"--full"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.Execute()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestPruneRawRejectsZeroNegativeFuture(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	database.Close()
	for _, bad := range []string{"0d", "-5d", "-1d", "abc"} {
		cmd := newPruneRawCmd()
		cmd.SetArgs([]string{"--older-than", bad, "--source", "all"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		if err := cmd.Execute(); err == nil {
			t.Fatalf("--older-than %q must be rejected", bad)
		}
		// And it must not have mutated: raw_json still present.
		d := reopen(t)
		if rawJSONOf(t, d, "sess-old")[0] == "" {
			t.Fatalf("--older-than %q blanked raw_json despite erroring", bad)
		}
		d.Close()
	}
}

func TestPruneRawVacuumRefusedUnderLockIsNoop(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	database.Close()
	lockPath, err := config.LockPath()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(lockPath), 0o755)
	lease, leader, err := lock.AcquireOrFollow(lockPath)
	if err != nil || !leader {
		t.Fatalf("could not hold lock: %v leader=%v", err, leader)
	}
	defer lease.Release()

	cmd := newPruneRawCmd()
	cmd.SetArgs([]string{"--older-than", "14d", "--vacuum", "--source", "all"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("--vacuum under held lock must fail")
	}
	// Full no-op: nothing pruned.
	d := reopen(t)
	defer d.Close()
	if rawJSONOf(t, d, "sess-old")[0] == "" {
		t.Fatal("vacuum-refused prune still blanked raw_json (must be a full no-op)")
	}
}

func TestPruneRawDryRunVacuumPrecedence(t *testing.T) {
	d0, _ := pruneSandbox(t, "sess-old", 30)
	d0.Close()
	// --dry-run --vacuum under a held lock: dry-run wins, no lock check, no change.
	lockPath, _ := config.LockPath()
	os.MkdirAll(filepath.Dir(lockPath), 0o755)
	lease, leader, err := lock.AcquireOrFollow(lockPath)
	if err != nil || !leader {
		t.Fatalf("lock: %v", err)
	}
	defer lease.Release()
	out := runPruneCmd(t, "--older-than", "14d", "--dry-run", "--vacuum", "--source", "all")
	if !strings.Contains(out, "would prune") {
		t.Fatalf("dry-run+vacuum should report a dry run, not fail on the lock:\n%s", out)
	}
	d := reopen(t)
	defer d.Close()
	if rawJSONOf(t, d, "sess-old")[0] == "" {
		t.Fatal("dry-run+vacuum mutated the DB")
	}
}

func TestPruneRawSkipsChangedFile(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	// Change the file on disk after indexing: the snapshot no longer matches, so
	// classifyPrunable must skip it as "changed".
	if err := os.WriteFile(path, []byte(`{"type":"user","uuid":"x","sessionId":"sess-old","message":{"role":"user","content":"changed"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	database.Close()
	out := runPruneCmd(t, "--older-than", "14d", "--source", "all")
	if !strings.Contains(out, "changed 1") {
		t.Fatalf("changed file should be skipped as 'changed':\n%s", out)
	}
	d := reopen(t)
	defer d.Close()
	if rawJSONOf(t, d, "sess-old")[0] == "" {
		t.Fatal("changed-file session was pruned")
	}
}

func TestPruneRestoreCodexRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	uuid := "0199aaaa-bbbb-4ccc-8ddd-000000000001"
	dir := filepath.Join(home, ".codex", "sessions", "2026", "01", "01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(dir, "rollout-2026-01-01T10-00-00-"+uuid+".jsonl")
	lines := `{"timestamp":"2026-01-01T10:00:00Z","type":"session_meta","payload":{"id":"` + uuid + `","cwd":"/p"}}
{"timestamp":"2026-01-01T10:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello codex"}]}}
`
	if err := os.WriteFile(rollout, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(home, "xdg", "clio", "db.sqlite")
	os.MkdirAll(filepath.Dir(dbPath), 0o755)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ing := ingest.NewWithBuiltinSources(database, nil)
	if _, err := ing.IngestAll(context.Background(), filepath.Join(home, ".claude", "projects"), false); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour).Unix()
	database.Exec(`UPDATE sessions SET ended_at=?, started_at=? WHERE uuid=?`, old, old, uuid)
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	if m, _, _ := mustPrune(t, database, cutoff, uuid); m == 0 {
		t.Fatal("codex session not pruned")
	}
	if rawJSONOf(t, database, uuid)[0] != "" {
		t.Fatal("codex raw_json not blanked")
	}
	database.Close()

	if out := runIndexFull(t); strings.Contains(out, "could not be restored") {
		t.Fatalf("codex restore failed:\n%s", out)
	}
	d2 := reopen(t)
	defer d2.Close()
	if rawJSONOf(t, d2, uuid)[0] == "" {
		t.Fatal("index --full did not restore codex raw_json")
	}
}

func TestPruneRestoreGeminiRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	sid := "gem-prune-1"
	dir := filepath.Join(home, ".gemini", "tmp", "h1", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	chat := filepath.Join(dir, "session-2026-01-01T10-00-aaaa.jsonl")
	meta := `{"sessionId":"` + sid + `","projectHash":"h1","startTime":"2026-01-01T10:00:00Z","lastUpdated":"2026-01-01T10:01:00Z","kind":"chat"}`
	set := `{"$set":{"messages":[{"id":"m1","timestamp":"2026-01-01T10:00:01Z","type":"user","content":[{"text":"hi gemini"}]}]}}`
	if err := os.WriteFile(chat, []byte(meta+"\n"+set+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(home, "xdg", "clio", "db.sqlite")
	os.MkdirAll(filepath.Dir(dbPath), 0o755)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ing := ingest.NewWithBuiltinSources(database, nil)
	if _, err := ing.IngestAll(context.Background(), filepath.Join(home, ".claude", "projects"), false); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour).Unix()
	database.Exec(`UPDATE sessions SET ended_at=?, started_at=? WHERE uuid=?`, old, old, sid)
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	if m, _, _ := mustPrune(t, database, cutoff, sid); m == 0 {
		t.Fatal("gemini session not pruned")
	}
	database.Close()
	if out := runIndexFull(t); strings.Contains(out, "could not be restored") {
		t.Fatalf("gemini restore failed:\n%s", out)
	}
	d2 := reopen(t)
	defer d2.Close()
	if rawJSONOf(t, d2, sid)[0] == "" {
		t.Fatal("index --full did not restore gemini raw_json")
	}
}

func TestPruneRawLeavesUsageAndTargetsByteIdentical(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	// Seed a usage row and a tool_target for the session.
	database.Exec(`INSERT INTO session_usage(session_uuid, source, model, input_tokens, total_tokens) VALUES ('sess-old','claude-code','m',5,5)`)
	var msgID int64
	database.QueryRow(`SELECT id FROM messages WHERE session_uuid='sess-old' LIMIT 1`).Scan(&msgID)
	database.Exec(`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (?,?,?,?,?)`, msgID, "sess-old", 1, "tool", "Bash")
	// Snapshot usage + targets.
	usageBefore := dumpTable(t, database, `SELECT input_tokens,total_tokens FROM session_usage WHERE session_uuid='sess-old'`)
	targetsBefore := dumpTable(t, database, `SELECT kind,value FROM tool_targets WHERE session_uuid='sess-old'`)
	if usageBefore == "" || targetsBefore == "" {
		t.Fatalf("seed empty: usage=%q targets=%q", usageBefore, targetsBefore)
	}
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	mustPrune(t, database, cutoff, "sess-old")
	if dumpTable(t, database, `SELECT input_tokens,total_tokens FROM session_usage WHERE session_uuid='sess-old'`) != usageBefore {
		t.Fatal("session_usage changed after prune")
	}
	if dumpTable(t, database, `SELECT kind,value FROM tool_targets WHERE session_uuid='sess-old'`) != targetsBefore {
		t.Fatal("tool_targets changed after prune")
	}
}

func dumpTable(t *testing.T, d *db.DB, q string) string {
	t.Helper()
	rows, err := d.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out := ""
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out += fmt.Sprintf("%v|", vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPruneRestoreUnreadableFileSurfacedNonZero(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	mustPrune(t, database, cutoff, "sess-old")
	database.Close()
	// Make the file present but unreadable: index --full cannot restore, and the
	// file still exists (not purged), so the residual check must exit non-zero.
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o600)
	cmd := newIndexCmd()
	cmd.SetArgs([]string{"--full"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("unrestored (unreadable-file) prune must exit non-zero")
	}
}

func TestPruneRawSkipsMissingFile(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	database.Close()
	os.Remove(path) // file gone → "missing" skip, raw_json intact
	out := runPruneCmd(t, "--older-than", "14d", "--source", "all")
	if !strings.Contains(out, "missing 1") {
		t.Fatalf("missing file should be skipped as 'missing':\n%s", out)
	}
	d := reopen(t)
	defer d.Close()
	// (session still present in DB; its raw_json must not have been blanked)
	if rawJSONOf(t, d, "sess-old")[0] == "" {
		t.Fatal("missing-file session was pruned")
	}
}

func TestPruneRawSkipsPendingBackfill(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	// Insert a tool_use message with NO tool_targets → pending backfill.
	database.Exec(`INSERT INTO messages(session_uuid, seq, role, content, raw_json) VALUES ('sess-old', 99, 'tool_use', 'Bash x', '{"e":1}')`)
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	// Even passed as an eligible pair, the in-tx revalidation must exclude it.
	pr, err := sessions.PruneRawJSON(context.Background(), database, cutoff, "all", "", prunePairs(t, database, "sess-old"), false)
	if err != nil {
		t.Fatal(err)
	}
	if pr.PrunedSessions != 0 {
		t.Fatalf("pending-backfill session pruned (n=%d), want 0", pr.PrunedSessions)
	}
}

func TestShowCommandOnPrunedSession(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	mustPrune(t, database, cutoff, "sess-old")
	database.Close()
	// The source file is unchanged, so openAndCatchUp's IngestAll skips it (no
	// re-ingest, no restore) — the pruned state persists through the show command.

	// --format json: RawJSON null + raw_pruned true.
	outJSON := runShowCmd(t, "sess-old", "--format", "json", "--source", "all")
	if !strings.Contains(outJSON, `"raw_pruned": true`) || !strings.Contains(outJSON, `"RawJSON": null`) {
		t.Fatalf("json show missing pruned markers:\n%s", outJSON)
	}
	// --format markdown: unaffected (no note, content still present).
	outMD := runShowCmd(t, "sess-old", "--format", "markdown", "--source", "all")
	if strings.Contains(outMD, "pruned") || !strings.Contains(outMD, "hello world") {
		t.Fatalf("markdown show altered by prune:\n%s", outMD)
	}
}

func runShowCmd(t *testing.T, args ...string) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	cmd := newShowCmd()
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	if err != nil {
		t.Fatalf("show failed: %v\n%s", err, buf.String())
	}
	return buf.String()
}

func TestPruneRawSkipsLagged(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	// Make the ingest snapshot lagged: offset < size (not fully ingested).
	database.Exec(`UPDATE ingest_state SET last_byte_offset = last_size - 1 WHERE source_file=?`, path)
	database.Close()
	out := runPruneCmd(t, "--older-than", "14d", "--source", "all")
	if !strings.Contains(out, "lagged 1") {
		t.Fatalf("lagged snapshot should be skipped:\n%s", out)
	}
	d := reopen(t)
	defer d.Close()
	if rawJSONOf(t, d, "sess-old")[0] == "" {
		t.Fatal("lagged session was pruned")
	}
}

func TestShowRawFormatPrunedNote(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	mustPrune(t, database, time.Now().Add(-14*24*time.Hour).Unix(), "sess-old")
	database.Close()
	// Capture stderr (the note goes there).
	oldErr := os.Stderr
	rerr, werr, _ := os.Pipe()
	os.Stderr = werr
	runShowCmd(t, "sess-old", "--format", "raw", "--source", "all")
	werr.Close()
	os.Stderr = oldErr
	var eb bytes.Buffer
	eb.ReadFrom(rerr)
	if !strings.Contains(eb.String(), "was pruned") || !strings.Contains(eb.String(), "clio index --full") {
		t.Fatalf("raw show missing per-session pruned note:\n%s", eb.String())
	}
}

func TestPruneRawTupleRaceRejectsMovedFile(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	// Build a pair with the CURRENT source_file, then simulate a concurrent
	// ingest that moved the session to a different source_file. The in-tx
	// (uuid,source_file) binding must reject the stale pair.
	pairs := prunePairs(t, database, "sess-old")
	database.Exec(`UPDATE sessions SET source_file='/moved/elsewhere.jsonl' WHERE uuid='sess-old'`)
	pr, err := sessions.PruneRawJSON(context.Background(), database, time.Now().Add(-14*24*time.Hour).Unix(), "all", "", pairs, false)
	if err != nil {
		t.Fatal(err)
	}
	if pr.PrunedSessions != 0 || pr.ValidatedSessions != 0 {
		t.Fatalf("moved-file session pruned=%d validated=%d want 0/0 (stale tuple rejected)", pr.PrunedSessions, pr.ValidatedSessions)
	}
	if pr.RacedOut != 1 {
		t.Fatalf("raced-out=%d want 1", pr.RacedOut)
	}
}

// A run with a preflight skip AND an idempotent (already-pruned) session must
// report a self-consistent "skipped N ... reason M" line (N == sum of reasons),
// not candidates-minus-pruned (which would double-count the idempotent one).
func TestPruneSummarySkipTotalConsistent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	proj := filepath.Join(home, ".claude", "projects", "-Users-x-proj")
	os.MkdirAll(proj, 0o755)
	// s-keep: restorable (will be pruned then rerun→idempotent). s-miss: file removed.
	for _, u := range []string{"s-keep", "s-miss"} {
		line := `{"type":"assistant","uuid":"a","timestamp":"2026-01-01T10:00:00Z","sessionId":"` + u + `","message":{"role":"assistant","model":"m","content":[{"type":"text","text":"hi"}]}}`
		os.WriteFile(filepath.Join(proj, u+".jsonl"), []byte(line+"\n"), 0o600)
	}
	dbPath := filepath.Join(home, "xdg", "clio", "db.sqlite")
	os.MkdirAll(filepath.Dir(dbPath), 0o755)
	database, _ := db.Open(dbPath)
	ing := ingest.New(database, nil)
	ing.IngestAll(context.Background(), proj, false)
	old := time.Now().Add(-30 * 24 * time.Hour).Unix()
	database.Exec(`UPDATE sessions SET ended_at=?, started_at=?`, old, old)
	// Prune s-keep once (so a rerun sees it idempotent), then remove s-miss's file.
	mustPrune(t, database, time.Now().Add(-14*24*time.Hour).Unix(), "s-keep")
	database.Close()
	os.Remove(filepath.Join(proj, "s-miss.jsonl"))

	out := runPruneCmd(t, "--older-than", "14d", "--source", "all")
	// Exactly one preflight skip (s-miss missing); s-keep is validated+idempotent
	// (0 newly pruned) but NOT a skip. The skipped total must equal the reason sum.
	if !strings.Contains(out, "skipped 1 of") || !strings.Contains(out, "missing 1") {
		t.Fatalf("skip total inconsistent with reasons:\n%s", out)
	}
}

func TestPruneRawSkipsUndiscoverable(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	// Move the file (and its ingest_state + sessions rows) to a location OUTSIDE
	// every scanned root, keeping a valid snapshot (aborted=0, offset==size). The
	// file exists and matches, but underAnyRoot fails → "undiscoverable".
	outside := filepath.Join(t.TempDir(), "out-of-root.jsonl")
	data, _ := os.ReadFile(path)
	os.WriteFile(outside, data, 0o600)
	fi, _ := os.Stat(outside)
	database.Exec(`UPDATE ingest_state SET source_file=?, last_size=?, last_mtime=?, last_byte_offset=? WHERE source_file=?`,
		outside, fi.Size(), fi.ModTime().UnixNano(), fi.Size(), path)
	database.Exec(`UPDATE sessions SET source_file=? WHERE uuid='sess-old'`, outside)
	os.Remove(path)
	database.Close()
	out := runPruneCmd(t, "--older-than", "14d", "--source", "all")
	if !strings.Contains(out, "undiscoverable 1") {
		t.Fatalf("out-of-root session should be skipped undiscoverable:\n%s", out)
	}
	d := reopen(t)
	defer d.Close()
	if rawJSONOf(t, d, "sess-old")[0] == "" {
		t.Fatal("undiscoverable session was pruned")
	}
}

// Command-level skip-reason counting for the remaining reasons, asserting the
// summary output (not just the sessions-layer refusal).
func TestPruneCommandLevelSkipReasons(t *testing.T) {
	t.Run("unverified", func(t *testing.T) {
		database, path := pruneSandbox(t, "sess-old", 30)
		database.Exec(`UPDATE ingest_state SET aborted=1 WHERE source_file=?`, path)
		database.Close()
		out := runPruneCmd(t, "--older-than", "14d", "--source", "all")
		if !strings.Contains(out, "unverified 1") {
			t.Fatalf("expected 'unverified 1':\n%s", out)
		}
	})
	t.Run("pending-backfill", func(t *testing.T) {
		database, _ := pruneSandbox(t, "sess-old", 30)
		database.Exec(`INSERT INTO messages(session_uuid, seq, role, content, raw_json) VALUES ('sess-old', 98, 'tool_use', 'Bash', '{"e":1}')`)
		database.Close()
		out := runPruneCmd(t, "--older-than", "14d", "--source", "all")
		if !strings.Contains(out, "pending-backfill 1") {
			t.Fatalf("expected 'pending-backfill 1':\n%s", out)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses file permissions; chmod 000 stays readable")
		}
		database, path := pruneSandbox(t, "sess-old", 30)
		database.Close()
		if err := os.Chmod(path, 0o000); err != nil {
			t.Skipf("chmod unsupported: %v", err)
		}
		defer os.Chmod(path, 0o600)
		out := runPruneCmd(t, "--older-than", "14d", "--source", "all")
		if !strings.Contains(out, "unreadable 1") {
			t.Fatalf("expected 'unreadable 1':\n%s", out)
		}
	})
}

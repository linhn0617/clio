package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

func TestReconcileDetectsTruncation(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sess.jsonl")
	if err := os.WriteFile(src, []byte("0123456789\n0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(dir, "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// Record state as if we ingested the full 22-byte file.
	if _, err := d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at) VALUES (?,?,?,?,?,?)`,
		src, 22, 1, 22, "fp", 1); err != nil {
		t.Fatal(err)
	}
	if m, _, tr, _, _, _ := reconcile(d, []string{dir}); m != 0 || tr != 0 {
		t.Fatalf("expected clean before truncation, got missing=%d truncated=%d", m, tr)
	}

	// Truncate the source file.
	if err := os.WriteFile(src, []byte("0123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, tr, _, _, _ := reconcile(d, []string{dir}); tr != 1 {
		t.Fatalf("expected 1 truncated, got %d", tr)
	}
}

// TestReconcileDetectsSameSizeRewrite covers defect (B): ingest treats a file whose
// mtime changed but whose size stayed the same as a full rewrite (see
// internal/ingest/incremental.go classifyChange, the "same size, new mtime" case) —
// doctor's reconciliation must catch this too, not just size shrink/growth.
func TestReconcileDetectsSameSizeRewrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sess.jsonl")
	initial := []byte("0123456789\n0123456789\n") // 22 bytes
	if err := os.WriteFile(src, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(dir, "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// Record state as if we ingested the full file at its real mtime.
	if _, err := d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at) VALUES (?,?,?,?,?,?)`,
		src, fi.Size(), fi.ModTime().UnixNano(), fi.Size(), "fp", 1); err != nil {
		t.Fatal(err)
	}
	if _, _, _, rw, _, _ := reconcile(d, []string{dir}); rw != 0 {
		t.Fatalf("expected clean before rewrite, got rewritten=%d", rw)
	}

	// Same-size rewrite: identical length, different content, distinct mtime (forced via
	// Chtimes so the assertion does not depend on filesystem mtime-clock resolution).
	replacement := []byte("9999999999\n0123456789\n")
	if len(replacement) != len(initial) {
		t.Fatalf("test fixture bug: replacement must be same size as initial (%d != %d)", len(replacement), len(initial))
	}
	if err := os.WriteFile(src, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	newMTime := fi.ModTime().Add(time.Second)
	if err := os.Chtimes(src, newMTime, newMTime); err != nil {
		t.Fatal(err)
	}
	if _, _, tr, rw, _, _ := reconcile(d, []string{dir}); tr != 0 || rw != 1 {
		t.Fatalf("expected the same-size rewrite detected (rewritten=1, truncated=0), got truncated=%d rewritten=%d", tr, rw)
	}
}

func TestReconcileDetectsMissing(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at) VALUES (?,?,?,?,?,?)`,
		filepath.Join(dir, "gone.jsonl"), 10, 1, 10, "fp", 1)
	if m, _, _, _, _, _ := reconcile(d, []string{dir}); m != 1 {
		t.Fatalf("expected 1 missing, got %d", m)
	}
}

func TestRunReportsChecks(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	results := Run(d, dir, dbPath)
	if len(results) == 0 {
		t.Fatal("expected check results")
	}
	// db integrity must pass on a fresh DB.
	var sawIntegrity bool
	for _, r := range results {
		if r.Name == "db integrity" {
			sawIntegrity = true
			if !r.OK {
				t.Fatal("fresh db should pass integrity")
			}
		}
	}
	if !sawIntegrity {
		t.Fatal("integrity check missing")
	}
}

func TestRunReportsUnparsedLines(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// Zero unparsed lines: the check passes.
	if r := findResult(t, Run(d, dir, dbPath), "unparsed lines"); !r.OK {
		t.Fatalf("expected pass with zero unparsed, got %+v", r)
	}

	// Record some unparsed lines for a source.
	if _, err := d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at,unparsed_lines) VALUES (?,?,?,?,?,?,?)`,
		filepath.Join(dir, "s.jsonl"), 10, 1, 10, "fp", 1, 3); err != nil {
		t.Fatal(err)
	}
	r := findResult(t, Run(d, dir, dbPath), "unparsed lines")
	if r.OK {
		t.Fatal("expected failing check when unparsed_lines > 0")
	}
	if !strings.Contains(r.Detail, "3") {
		t.Fatalf("expected the count in detail, got %q", r.Detail)
	}
}

// The DB file (and its sidecars) must be 0600; a world-readable mode is flagged.
func TestRunFlagsWorldReadableDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if r := findResult(t, Run(d, dir, dbPath), "file permissions"); !r.OK {
		t.Fatalf("fresh 0600 db should pass perms, got %+v", r)
	}
	if err := os.Chmod(dbPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := findResult(t, Run(d, dir, dbPath), "file permissions"); r.OK {
		t.Fatalf("expected a perm warning for a 0644 db, got %+v", r)
	}
}

// On a pre-0005 DB (doctor opens read-only, no migration), the unparsed_lines column
// may not exist yet; the check must tolerate that as legacy 0, not warn.
func TestRunToleratesMissingUnparsedColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`ALTER TABLE ingest_state DROP COLUMN unparsed_lines`); err != nil {
		t.Skipf("DROP COLUMN unsupported: %v", err)
	}
	if r := findResult(t, Run(d, dir, dbPath), "unparsed lines"); !r.OK {
		t.Fatalf("expected tolerant pass on pre-migration db, got %+v", r)
	}
}

func findResult(t *testing.T, results []Result, name string) Result {
	t.Helper()
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("result %q not found in %+v", name, results)
	return Result{}
}

func TestRunFlagsFtsCheckWhenMessagesQueryErrors(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("DROP TABLE messages"); err != nil {
		t.Fatalf("drop messages: %v", err)
	}
	r := findResult(t, Run(d, dir, dbPath), "fts index")
	if r.OK {
		t.Fatalf("fts index must fail when the messages count query errors; got %+v", r)
	}
}

func TestRunFlagsReconciliationWhenIngestStateQueryErrors(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("DROP TABLE ingest_state"); err != nil {
		t.Fatalf("drop ingest_state: %v", err)
	}
	r := findResult(t, Run(d, dir, dbPath), "source reconciliation")
	if r.OK {
		t.Fatalf("source reconciliation must fail when the ingest_state query errors; got %+v", r)
	}
}

// TestReconcileFlagsUnverifiable: a tracked source path whose parent component is a
// regular file (not a directory) makes os.Stat return a non-IsNotExist error
// (ENOTDIR), independent of uid/permissions. reconcile must flag it (count it as
// missing) rather than silently skip it, so `source reconciliation` does not
// false-green on an unverifiable file.
func TestReconcileFlagsUnverifiable(t *testing.T) {
	dir := t.TempDir()
	notDir := filepath.Join(dir, "afile")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(dir, "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at) VALUES (?,?,?,?,?,?)`,
		filepath.Join(notDir, "child.jsonl"), 10, 1, 10, "fp", 1)
	m, _, _, _, _, rerr := reconcile(d, []string{dir})
	if rerr != nil {
		t.Fatalf("unexpected error: %v", rerr)
	}
	if m != 1 {
		t.Fatalf("expected unverifiable file flagged (missing=1), got %d", m)
	}
}

func TestReconcilePreservesFilesUnderUnavailableRoot(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// A tracked file whose source root is NOT among the available roots: its absence
	// is preservation (the root is unavailable), not a deletion.
	d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at) VALUES (?,?,?,?,?,?)`,
		filepath.Join(dir, "unavailable-root", "rollout-x.jsonl"), 10, 1, 10, "fp", 1)
	m, preserved, _, _, _, rerr := reconcile(d, []string{filepath.Join(dir, "available-root")})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if m != 0 || preserved != 1 {
		t.Fatalf("expected the file under an unavailable root preserved, got missing=%d preserved=%d", m, preserved)
	}
}

// TestRunFailsWhenReconciliationHasUnindexedBytes covers defect (A): doctor's detail
// text already reports "N with new unindexed bytes" (lag) but the OK verdict ignored
// it, so a source with unindexed bytes could still be reported healthy (exit 0). A
// file with content beyond the last recorded offset must flip "source reconciliation"
// (and thus overall health) to not-OK.
func TestRunFailsWhenReconciliationHasUnindexedBytes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sess.jsonl")
	if err := os.WriteFile(src, []byte("0123456789\n0123456789\n"), 0o600); err != nil { // 22 bytes
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// Only the first 11 bytes were ever ingested: 11 bytes of new content are lagging.
	if _, err := d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at) VALUES (?,?,?,?,?,?)`,
		src, 11, 1, 11, "fp", 1); err != nil {
		t.Fatal(err)
	}

	r := findResult(t, Run(d, dir, dbPath), "source reconciliation")
	if r.OK {
		t.Fatalf("expected source reconciliation to fail when there are unindexed bytes, got %+v", r)
	}
	if !strings.Contains(r.Detail, "unindexed bytes") {
		t.Fatalf("expected detail to mention unindexed bytes, got %q", r.Detail)
	}
}

// TestCoverageBySourceIsolatesGapPerSource covers defect (C)'s coverage half: a well-
// tracked Codex source must not mask a coverage gap in Claude Code files. Comparing
// disk-vs-tracked totals across all sources combined would hide this (8 Claude files
// on disk / 2 tracked, but 500 Codex rows tracked, net "2+500 tracked >= on-disk" looks
// fine); comparing per source root must not.
func TestCoverageBySourceIsolatesGapPerSource(t *testing.T) {
	dir := t.TempDir()
	claudeRoot := filepath.Join(dir, "claude-projects")
	codexRoot := filepath.Join(dir, "codex-sessions")
	if err := os.MkdirAll(claudeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	// 8 Claude session files on disk.
	for i := 0; i < 8; i++ {
		if err := os.WriteFile(filepath.Join(claudeRoot, fmt.Sprintf("s%d.jsonl", i)), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	d, err := db.Open(filepath.Join(dir, "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// Only 2 of the 8 Claude files are actually tracked.
	for i := 0; i < 2; i++ {
		if _, err := d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at) VALUES (?,?,?,?,?,?)`,
			filepath.Join(claudeRoot, fmt.Sprintf("s%d.jsonl", i)), 3, 1, 3, "fp", 1); err != nil {
			t.Fatal(err)
		}
	}
	// 500 unrelated, fully-tracked Codex rows: large enough to mask the Claude gap
	// under a combined-totals comparison (8 on disk <= 2+500 tracked).
	for i := 0; i < 500; i++ {
		if _, err := d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at) VALUES (?,?,?,?,?,?)`,
			filepath.Join(codexRoot, fmt.Sprintf("rollout-%d.jsonl", i)), 3, 1, 3, "fp", 1); err != nil {
			t.Fatal(err)
		}
	}

	ok, detail, err := coverageBySource(d, []string{claudeRoot, codexRoot})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected the Claude coverage gap to be caught despite full Codex coverage, got ok=true detail=%q", detail)
	}
}

// TestCoverageBySourceOKWhenFullyTracked is the counterpart green case: every source
// fully covered must report ok.
func TestCoverageBySourceOKWhenFullyTracked(t *testing.T) {
	dir := t.TempDir()
	claudeRoot := filepath.Join(dir, "claude-projects")
	if err := os.MkdirAll(claudeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeRoot, "s0.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(dir, "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at) VALUES (?,?,?,?,?,?)`,
		filepath.Join(claudeRoot, "s0.jsonl"), 3, 1, 3, "fp", 1); err != nil {
		t.Fatal(err)
	}
	ok, detail, err := coverageBySource(d, []string{claudeRoot})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected fully-tracked single source to be ok, got detail=%q", detail)
	}
}

// TestClaudeDirStatus covers defect (C)'s severity half: a missing ~/.claude/projects
// is only a real problem when no other source is available. A Codex-only install
// (Codex present, Claude absent) is a supported configuration and must not warn.
func TestClaudeDirStatus(t *testing.T) {
	cases := []struct {
		name         string
		present      bool
		codexPresent bool
		wantOK       bool
	}{
		{"present", true, false, true},
		{"present and codex too", true, true, true},
		{"absent, codex-only install", false, true, true},
		{"absent, no other source", false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, _ := claudeDirStatus(c.present, c.codexPresent)
			if ok != c.wantOK {
				t.Fatalf("claudeDirStatus(present=%v, codexPresent=%v) ok=%v, want %v", c.present, c.codexPresent, ok, c.wantOK)
			}
		})
	}
}

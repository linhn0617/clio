package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if m, tr, _, _ := reconcile(d); m != 0 || tr != 0 {
		t.Fatalf("expected clean before truncation, got missing=%d truncated=%d", m, tr)
	}

	// Truncate the source file.
	if err := os.WriteFile(src, []byte("0123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, tr, _, _ := reconcile(d); tr != 1 {
		t.Fatalf("expected 1 truncated, got %d", tr)
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
	if m, _, _, _ := reconcile(d); m != 1 {
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
	m, _, _, rerr := reconcile(d)
	if rerr != nil {
		t.Fatalf("unexpected error: %v", rerr)
	}
	if m != 1 {
		t.Fatalf("expected unverifiable file flagged (missing=1), got %d", m)
	}
}

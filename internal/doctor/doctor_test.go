package doctor

import (
	"os"
	"path/filepath"
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

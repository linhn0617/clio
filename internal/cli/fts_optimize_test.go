package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)

func ftsDataRows(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM messages_fts_data`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// churnFTS re-ingests the sandbox session several times (full = delete + reinsert), the
// way repeated `clio index --full` runs pile up FTS5 segments and tombstones.
func churnFTS(t *testing.T, d *db.DB, rounds int) {
	t.Helper()
	var src string
	if err := d.QueryRow(`SELECT source_file FROM ingest_state LIMIT 1`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	projects := src[:strings.LastIndex(src, "/")]
	projects = projects[:strings.LastIndex(projects, "/")]
	ing := ingest.New(d, nil)
	for i := 0; i < rounds; i++ {
		if _, err := ing.IngestAll(context.Background(), projects, true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOptimizeFTSShrinksSegments(t *testing.T) {
	d, _ := pruneSandbox(t, "sess-fts", 1)
	churnFTS(t, d, 6)
	before := ftsDataRows(t, d)
	if err := optimizeFTS(context.Background(), d); err != nil {
		t.Fatalf("optimizeFTS: %v", err)
	}
	after := ftsDataRows(t, d)
	if after >= before {
		t.Fatalf("optimize did not reduce messages_fts_data rows: before=%d after=%d", before, after)
	}
}

func TestIndexFullOptimizesFTS(t *testing.T) {
	d, _ := pruneSandbox(t, "sess-fts", 1)
	churnFTS(t, d, 6)
	before := ftsDataRows(t, d)
	d.Close()
	out, err := runIndexCmd(t, "--full")
	if err != nil {
		t.Fatalf("index --full: %v\n%s", err, out)
	}
	if !strings.Contains(out, "optimizing fts index") || !strings.Contains(out, "fts optimized") {
		t.Fatalf("missing optimize output:\n%s", out)
	}
	d2 := reopen(t)
	defer d2.Close()
	if after := ftsDataRows(t, d2); after >= before {
		t.Fatalf("index --full left fts segments unmerged: before=%d after=%d", before, after)
	}
}

func TestIncrementalIndexDoesNotOptimize(t *testing.T) {
	d, _ := pruneSandbox(t, "sess-fts", 1)
	d.Close()
	out, err := runIndexCmd(t)
	if err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	if strings.Contains(out, "fts") {
		t.Fatalf("incremental index must not optimize:\n%s", out)
	}
}

func TestPruneVacuumOptimizesFTS(t *testing.T) {
	d, _ := pruneSandbox(t, "sess-old", 30)
	churnFTS(t, d, 6)
	before := ftsDataRows(t, d)
	d.Close()
	out := runPruneCmd(t, "--older-than", "14d", "--vacuum", "--source", "all")
	if !strings.Contains(out, "fts optimized") {
		t.Fatalf("prune --vacuum did not report optimize:\n%s", out)
	}
	d2 := reopen(t)
	defer d2.Close()
	if after := ftsDataRows(t, d2); after >= before {
		t.Fatalf("prune --vacuum left fts segments unmerged: before=%d after=%d", before, after)
	}
}

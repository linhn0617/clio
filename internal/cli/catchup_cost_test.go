package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)

// TestRunCatchUpAlwaysBackfills ensures upgrades backfill even without new files.
func TestRunCatchUpAlwaysBackfills(t *testing.T) {
	for _, c := range []struct {
		name         string
		stats        ingest.Stats
		wantBackfill int32
	}{
		{"nothing new", ingest.Stats{FilesScanned: 40, FilesSkipped: 40}, 1},
		{"new content", ingest.Stats{FilesScanned: 40, FilesSkipped: 39, FilesIngested: 1, MessagesAdded: 3}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeIngester{stats: c.stats}
			runCatchUp(context.Background(), f, "/nonexistent", discardLogger())
			if got := f.backfill.Load(); got != c.wantBackfill {
				t.Fatalf("BackfillActivity calls = %d, want %d", got, c.wantBackfill)
			}
			if f.ingestAll.Load() != 1 || f.purge.Load() != 1 {
				t.Fatalf("ingestAll=%d purge=%d, want 1/1", f.ingestAll.Load(), f.purge.Load())
			}
		})
	}
}

// TestRunIndexIngestAlwaysBackfills pins that `clio index` keeps the unconditional
// backfill as the safety net, even when nothing new was ingested.
func TestRunIndexIngestAlwaysBackfills(t *testing.T) {
	for _, full := range []bool{false, true} {
		f := &fakeIngester{stats: ingest.Stats{FilesScanned: 5, FilesSkipped: 5}}
		if _, err := runIndexIngest(context.Background(), f, "/nonexistent", full, discardLogger()); err != nil {
			t.Fatal(err)
		}
		if got := f.backfill.Load(); got != 1 {
			t.Fatalf("full=%v: BackfillActivity calls = %d, want 1", full, got)
		}
	}
}

func TestRunCatchUpUpgradeActivity(t *testing.T) {
	projects := t.TempDir()
	dir := filepath.Join(projects, "project")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "upgrade.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"assistant","sessionId":"upgrade","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a"}}]}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ing := ingest.New(d, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`DELETE FROM tool_targets`); err != nil {
		t.Fatal(err)
	}
	runCatchUp(context.Background(), ing, projects, discardLogger())
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM tool_targets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("upgrade targets = %d, want 2", n)
	}
	var w, maxID int64
	if err := d.QueryRow(`SELECT checked_through,(SELECT MAX(id) FROM messages) FROM activity_backfill_state WHERE id=1`).Scan(&w, &maxID); err != nil {
		t.Fatal(err)
	}
	if w != maxID {
		t.Fatalf("upgrade watermark %d != max %d", w, maxID)
	}
}

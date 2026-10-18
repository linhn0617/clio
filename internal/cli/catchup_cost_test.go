package cli

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)

// TestRunCatchUpAlwaysBackfills ensures upgrades backfill even without new files.
func TestRunCatchUpAlwaysBackfills(t *testing.T) {
	f := &fakeIngester{}
	runCatchUp(context.Background(), f, "/nonexistent", discardLogger())
	if got := f.backfill.Load(); got != 1 {
		t.Fatalf("BackfillActivity calls = %d, want 1", got)
	}
	if f.ingestAll.Load() != 1 || f.purge.Load() != 1 {
		t.Fatalf("ingestAll=%d purge=%d, want 1/1", f.ingestAll.Load(), f.purge.Load())
	}
	if f.repair.Load() != 0 {
		t.Fatal("read path invoked full repair")
	}
}

// TestRunIndexIngestAlwaysBackfills pins that `clio index` keeps the unconditional
// backfill as the safety net, even when nothing new was ingested.
func TestRunIndexIngestAlwaysBackfills(t *testing.T) {
	for _, full := range []bool{false, true} {
		f := &fakeIngester{}
		if _, err := runIndexIngest(context.Background(), f, "/nonexistent", full, discardLogger()); err != nil {
			t.Fatal(err)
		}
		if got := f.repair.Load(); got != 1 {
			t.Fatalf("full=%v: RepairActivity calls = %d, want 1", full, got)
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

func TestIndexRepairsBelowWatermark(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			d, err := db.Open(filepath.Join(t.TempDir(), "repair.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			// No transcript is discoverable: --full must repair from stored raw_json,
			// rather than rebuild targets incidentally during file ingestion.
			for _, q := range []string{
				`INSERT INTO sessions(uuid,source_file) VALUES ('repair','missing')`,
				`INSERT INTO messages(session_uuid,seq,role,content,raw_json) VALUES ('repair',0,'tool_use','','{"message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/repair"}}]}}')`,
				`INSERT INTO activity_backfill_state(id,checked_through) VALUES (1,1)`,
			} {
				if _, err := d.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			ing := ingest.New(d, nil)
			runCatchUp(context.Background(), ing, t.TempDir(), discardLogger())
			var n int
			if err := d.QueryRow(`SELECT count(*) FROM tool_targets`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("read repaired historical row: %d targets", n)
			}
			if _, err := runIndexIngest(context.Background(), ing, t.TempDir(), full, discardLogger()); err != nil {
				t.Fatal(err)
			}
			if err := d.QueryRow(`SELECT count(*) FROM tool_targets WHERE value IN ('Read','/repair')`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 2 {
				t.Fatalf("index full=%v: repaired targets = %d, want 2", full, n)
			}
			if _, err := runIndexIngest(context.Background(), ing, t.TempDir(), full, discardLogger()); err != nil {
				t.Fatal(err)
			}
			if err := d.QueryRow(`SELECT count(*) FROM tool_targets`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 2 {
				t.Fatalf("repair repeated targets: %d, want 2", n)
			}
		})
	}
}

func TestReadBackfillBusyAdvance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.sqlite")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`INSERT INTO sessions(uuid,source_file) VALUES ('busy','unused'); INSERT INTO messages(session_uuid,seq,role,content,raw_json) VALUES ('busy',0,'user','','{}')`); err != nil {
		t.Fatal(err)
	}
	other, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	// Isolate watermark advancement from PurgeMissing's existing write transaction
	// by keeping the transcript root unavailable.
	start := time.Now()
	runCatchUp(context.Background(), ingest.New(d, nil), filepath.Join(t.TempDir(), "missing"), log)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("read backfill waited for writer: %v", elapsed)
	}
	if strings.Contains(logs.String(), "WARN") {
		t.Errorf("read backfill warned under contention: %s", logs.String())
	}
	var timeout int
	if err := d.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != 3000 {
		t.Fatalf("busy timeout not restored: %d", timeout)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := ingest.New(d, nil).BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	var w int
	if err := d.QueryRow(`SELECT checked_through FROM activity_backfill_state WHERE id=1`).Scan(&w); err != nil {
		t.Fatal(err)
	}
	if w != 1 {
		t.Fatalf("retry watermark = %d, want 1", w)
	}
}

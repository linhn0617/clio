package cli

import (
	"context"
	"testing"

	"github.com/linhn0617/clio/internal/ingest"
)

// TestRunCatchUpBackfillOnlyWhenIngested pins that the read-path catch-up pays for the
// activity-backfill pre-check (a correlated scan over every tool_use message) only when
// this catch-up actually ingested something.
func TestRunCatchUpBackfillOnlyWhenIngested(t *testing.T) {
	for _, c := range []struct {
		name         string
		stats        ingest.Stats
		wantBackfill int32
	}{
		{"nothing new", ingest.Stats{FilesScanned: 40, FilesSkipped: 40}, 0},
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

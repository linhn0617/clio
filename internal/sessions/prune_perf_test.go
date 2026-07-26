package sessions

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

// walPageNumbers parses a SQLite -wal file and returns the set of DB page
// numbers that have frames in it. WAL format: 32-byte header (page size at
// offset 8, big-endian), then frames of a 24-byte frame-header (page number at
// offset 0, big-endian) + one page of data.
func walPageNumbers(t *testing.T, walPath string) (pages map[int64]bool, frames int) {
	t.Helper()
	b, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	pages = map[int64]bool{}
	if len(b) < 32 {
		return pages, 0
	}
	pageSize := int64(binary.BigEndian.Uint32(b[8:12]))
	if pageSize <= 0 {
		t.Fatalf("bad wal page size %d", pageSize)
	}
	frame := int64(24) + pageSize
	for off := int64(32); off+frame <= int64(len(b)); off += frame {
		pages[int64(binary.BigEndian.Uint32(b[off:off+4]))] = true
		frames++
	}
	return pages, frames
}

func ftsPageSet(t *testing.T, d *db.DB) map[int64]bool {
	t.Helper()
	out := map[int64]bool{}
	for _, tbl := range []string{"messages_fts_data", "messages_fts_idx", "messages_fts_docsize"} {
		rows, err := d.Query(`SELECT pageno FROM dbstat WHERE name=?`, tbl)
		if err != nil {
			t.Fatalf("dbstat %s: %v", tbl, err)
		}
		for rows.Next() {
			var p int64
			if err := rows.Scan(&p); err != nil {
				t.Fatalf("scan dbstat %s: %v", tbl, err)
			}
			out[p] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("dbstat %s rows: %v", tbl, err)
		}
		rows.Close()
	}
	return out
}

func tablePageCount(t *testing.T, d *db.DB, name string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM dbstat WHERE name=?`, name).Scan(&n); err != nil {
		t.Fatalf("dbstat count %s: %v", name, err)
	}
	return n
}

// TestPrunePerfGates asserts the three falsifiable prune gates from design.md:
// (a) the prune's WAL page set is DISJOINT from the FTS table pages (FTS bytes
// provably not rewritten — stronger than an unchanged page count); (b) on a
// prune-everything fixture the WAL frames are ≤ 3× the messages-table page
// count (writes confined to the messages heap); (c) post-checkpoint+VACUUM the
// DB shrinks by ≥ 90% of the reported removed bytes.
func TestPrunePerfGates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "perf.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Disable autocheckpoint so the prune's frames stay in the WAL for parsing.
	if _, err := d.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}

	// Fixture: many messages, each with a large raw_json so removal is measurable.
	old := time.Now().Add(-90 * 24 * time.Hour).Unix()
	big := make([]byte, 2048)
	for i := range big {
		big[i] = 'x'
	}
	if _, err := d.Exec(`INSERT INTO sessions(uuid, source_file, ended_at, started_at, turn_count) VALUES ('s1','f1.jsonl',?,?,1)`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, last_ingested_at, aborted) VALUES ('f1.jsonl',1,1,1,'',1,0)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		if _, err := d.Exec(`INSERT INTO messages(session_uuid, seq, role, content, raw_json) VALUES ('s1',?,'assistant',?,?)`,
			i, fmt.Sprintf("content %d searchable", i), `{"raw":"`+string(big)+`"}`); err != nil {
			t.Fatal(err)
		}
	}

	sizeBefore := dbFileBytes(t, d)
	if _, err := d.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	ftsPagesBefore := ftsPageSet(t, d)
	if len(ftsPagesBefore) == 0 {
		t.Fatal("fixture has no FTS pages — gate (a) would be vacuous")
	}
	msgPages := tablePageCount(t, d, "messages")

	// The prune (blank all raw_json), leaving frames in the WAL.
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	pr, err := PruneRawJSON(context.Background(), d, cutoff, "all", "", []PrunePair{{UUID: "s1", SourceFile: "f1.jsonl"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Messages != 2000 || pr.RemovedBytes == 0 {
		t.Fatalf("prune msgs=%d removed=%d want 2000 / >0", pr.Messages, pr.RemovedBytes)
	}

	walPages, walFrames := walPageNumbers(t, dbPath+"-wal")
	if walFrames == 0 {
		// autocheckpoint is disabled above, so an empty WAL means the prune wrote
		// nothing to it — that is itself a measurement failure, not a skip.
		t.Fatal("WAL empty after prune (autocheckpoint disabled) — cannot verify gates")
	}

	// FTS page set as it stands AFTER the prune too, so any newly-allocated FTS
	// page is included in the disjointness check (not just pre-prune pages).
	ftsPagesAfter := ftsPageSet(t, d)

	// Gate (a): the prune's WAL pages touch NONE of the FTS pages (before or after).
	for p := range walPages {
		if ftsPagesBefore[p] || ftsPagesAfter[p] {
			t.Fatalf("gate (a) FAIL: prune wrote FTS page %d — the FTS index was touched", p)
		}
	}
	// Gate (b): WAL FRAMES (not distinct pages) ≤ 3× messages-table pages.
	if walFrames > 3*msgPages+2 { // +2 slack for page1/freelist
		t.Fatalf("gate (b) FAIL: WAL frames %d > 3× messages pages %d", walFrames, msgPages)
	}

	// Gate (c): post-checkpoint+VACUUM shrink ≥ 90% of removed bytes.
	if _, err := d.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`VACUUM`); err != nil {
		t.Fatal(err)
	}
	sizeAfter := dbFileBytes(t, d)
	shrink := sizeBefore - sizeAfter
	if float64(shrink) < 0.9*float64(pr.RemovedBytes) {
		t.Fatalf("gate (c) FAIL: shrank %d bytes, < 90%% of removed %d", shrink, pr.RemovedBytes)
	}
}

func dbFileBytes(t *testing.T, d *db.DB) int64 {
	t.Helper()
	var pageCount, pageSize int64
	d.QueryRow(`PRAGMA page_count`).Scan(&pageCount)
	d.QueryRow(`PRAGMA page_size`).Scan(&pageSize)
	return pageCount * pageSize
}

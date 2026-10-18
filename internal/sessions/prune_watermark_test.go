package sessions

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)

func TestPruneBackfillWatermark(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "prune.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exec := func(q string) {
		t.Helper()
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sessions(uuid,source_file,started_at) VALUES ('old','old.jsonl',1),('new','new.jsonl',1)`)
	exec(`INSERT INTO ingest_state(source_file,last_size,last_mtime,last_byte_offset,tail_fingerprint,last_ingested_at,aborted) VALUES ('old.jsonl',1,1,1,'',1,0),('new.jsonl',1,1,1,'',1,0)`)
	exec(`INSERT INTO messages(session_uuid,seq,role,content,raw_json) VALUES ('old',0,'tool_use','','invalid')`)
	if err := ingest.New(d, nil).BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO messages(session_uuid,seq,role,content,raw_json) VALUES ('new',0,'tool_use','','{"message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/needed"}}]}}')`)
	pending, err := PendingBackfillSessions(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if pending["old"] || !pending["new"] {
		t.Errorf("pending = %v, want only new (unexamined)", pending)
	}
	// Pass both pairs to exercise the authoritative guard, even if preflight is stale.
	res, err := PruneRawJSON(context.Background(), d, 10, "all", "", []PrunePair{{"old", "old.jsonl"}, {"new", "new.jsonl"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.PrunedSessions != 1 || res.RacedOut != 1 {
		t.Errorf("prune = %+v, want one pruned / one protected", res)
	}
	var raw string
	if err := d.QueryRow(`SELECT raw_json FROM messages WHERE session_uuid='new'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "" {
		t.Fatal("pruned raw_json still needed by future backfill")
	}
	if err := ingest.New(d, nil).BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM tool_targets WHERE session_uuid='new'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("protected row backfill targets = %d, want 2", n)
	}
}

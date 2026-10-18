package ingest

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/model"
)

const watermarkEvent = `{"message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a"}},{"type":"tool_use","name":"Read","input":{"file_path":"/b"}}]}}`

func activityExec(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func activityWatermark(t *testing.T, d *db.DB) int64 {
	t.Helper()
	var w int64
	if err := d.QueryRow(`SELECT COALESCE((SELECT checked_through FROM activity_backfill_state WHERE id=1),0)`).Scan(&w); err != nil {
		t.Fatal(err)
	}
	return w
}

func watermarkFixture(t *testing.T) *db.DB {
	t.Helper()
	d := openTestDB(t)
	activityExec(t, d, `INSERT INTO sessions(uuid,source_file) VALUES ('wm','unused')`)
	activityExec(t, d, `INSERT INTO messages(session_uuid,seq,role,content,raw_json) VALUES ('wm',0,'tool_use','','invalid')`)
	return d
}

func TestActivityWatermarkUnfillableAndNoWrite(t *testing.T) {
	d := watermarkFixture(t)
	ing := New(d, nil)
	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w := activityWatermark(t, d); w != 1 {
		t.Fatalf("watermark = %d, want 1", w)
	}
	activityExec(t, d, `PRAGMA query_only=ON`)
	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatalf("no-op attempted write transaction: %v", err)
	}
	if w := activityWatermark(t, d); w != 1 {
		t.Fatalf("no-op watermark = %d", w)
	}
}

func TestActivityWatermarkFailureRetry(t *testing.T) {
	d := watermarkFixture(t)
	ing := New(d, nil)
	activityExec(t, d, `UPDATE messages SET raw_json=?`, watermarkEvent)
	activityExec(t, d, `CREATE TRIGGER fail_activity BEFORE INSERT ON tool_targets WHEN new.kind='file' BEGIN SELECT RAISE(ABORT,'injected backfill failure'); END`)
	if err := ing.BackfillActivity(context.Background()); err == nil || !strings.Contains(err.Error(), "injected backfill failure") {
		t.Fatalf("expected injected failure, got %v", err)
	}
	if w := activityWatermark(t, d); w != 0 {
		t.Fatalf("failed backfill advanced watermark to %d", w)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM tool_targets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("failed backfill left %d targets", n)
	}
	activityExec(t, d, `DROP TRIGGER fail_activity`)
	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w := activityWatermark(t, d); w != 1 {
		t.Fatalf("retry watermark = %d, want 1", w)
	}
}

func TestActivityWatermarkNewRowsAndSplitEvent(t *testing.T) {
	d := watermarkFixture(t)
	ing := New(d, nil)
	activityExec(t, d, `UPDATE messages SET raw_json=?`, watermarkEvent)
	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w := activityWatermark(t, d); w != 1 {
		t.Fatalf("watermark = %d, want 1", w)
	}
	// A historical missing fact must not cause a re-examination below W.
	activityExec(t, d, `DELETE FROM tool_targets`)
	activityExec(t, d, `INSERT INTO messages(session_uuid,seq,role,content,raw_json) VALUES ('wm',1,'tool_use','',?)`, watermarkEvent)
	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := d.QueryRow(`SELECT value FROM tool_targets WHERE message_id=2 AND kind='file'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "/b" {
		t.Fatalf("split-event target = %q, want /b", got)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM tool_targets WHERE message_id=1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("re-examined old message: %d targets", n)
	}
	if w := activityWatermark(t, d); w != 2 {
		t.Fatalf("new watermark = %d, want 2", w)
	}
}

func TestActivityWatermarkAlreadyFilled(t *testing.T) {
	d := watermarkFixture(t)
	activityExec(t, d, `INSERT INTO tool_targets(message_id,session_uuid,kind,value) VALUES (1,'wm','tool','Read')`)
	if err := New(d, nil).BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w := activityWatermark(t, d); w != 1 {
		t.Fatalf("filled watermark = %d, want 1", w)
	}
	activityExec(t, d, `PRAGMA query_only=ON`)
	if err := New(d, nil).BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestActivityWatermarkReplacementIDs(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "project", "replace", `{"type":"assistant","sessionId":"replace","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a"}}]}}`)
	d := openTestDB(t)
	ing := New(d, nil)
	if _, _, err := ing.IngestFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := activityWatermark(t, d)
	if w == 0 {
		t.Fatal("no initial watermark")
	}
	for _, purge := range []bool{false, true} {
		if purge {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if _, err := ing.DeleteMissing(context.Background(), projects); err != nil {
				t.Fatal(err)
			}
			var remaining int
			if err := d.QueryRow(`SELECT count(*) FROM messages`).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining != 0 {
				t.Fatalf("purge left %d messages", remaining)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := d.QueryRow(`SELECT MIN(id) FROM messages`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if id <= w {
			t.Fatalf("replacement id %d <= watermark %d", id, w)
		}
		activityExec(t, d, `DELETE FROM tool_targets`)
		if err := ing.BackfillActivity(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(activityRows(t, d, "replace")) != 2 {
			t.Fatal("replacement not backfilled")
		}
		w = activityWatermark(t, d)
	}
}

func TestActivityWatermarkPrimaryKeyPlan(t *testing.T) {
	d := watermarkFixture(t)
	rows, err := d.Query(`EXPLAIN QUERY PLAN `+activityPendingSQL, 1, model.RoleToolUse)
	if err != nil {
		t.Fatal(err)
	}
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Log(plan)
	if !strings.Contains(plan, "SEARCH m USING INTEGER PRIMARY KEY (rowid>?)") {
		t.Fatalf("pending check lacks primary key range:\n%s", plan)
	}
	if strings.Contains(plan, "SCAN m") {
		t.Fatalf("pending check scans messages:\n%s", plan)
	}
}

func TestActivityWatermarkIdenticalEvents(t *testing.T) {
	d := watermarkFixture(t)
	activityExec(t, d, `UPDATE messages SET raw_json=?`, watermarkEvent)
	activityExec(t, d, `INSERT INTO messages(session_uuid,seq,role,content,raw_json) VALUES ('wm',1,'tool_use','',?),('wm',2,'tool_use','',?)`, watermarkEvent, watermarkEvent)
	if err := New(d, nil).BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	activityExec(t, d, `INSERT INTO messages(session_uuid,seq,role,content,raw_json) VALUES ('wm',3,'tool_use','',?)`, watermarkEvent)
	if err := New(d, nil).BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := d.QueryRow(`SELECT value FROM tool_targets WHERE message_id=4 AND kind='file'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "/b" {
		t.Fatalf("identical adjacent event target = %q, want /b", got)
	}
}

func TestActivityWatermarkDifferentPredecessor(t *testing.T) {
	d := watermarkFixture(t)
	ing := New(d, nil)
	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	activityExec(t, d, `INSERT INTO messages(session_uuid,seq,role,content,raw_json) VALUES ('wm',1,'tool_use','',?)`, watermarkEvent)
	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := d.QueryRow(`SELECT value FROM tool_targets WHERE message_id=2 AND kind='file'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "/a" {
		t.Fatalf("new event target = %q, want /a", got)
	}
}

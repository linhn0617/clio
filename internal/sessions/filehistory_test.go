package sessions

import (
	"context"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/db"
)

// addTouch records one tool call against a file the way ingest does: a tool_use
// message, its tool_calls row, and a kind=file tool_targets row sharing the id.
func addTouch(t *testing.T, d *db.DB, sess, tool, path string, ts any) {
	t.Helper()
	res, err := d.Exec(`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES (?, (SELECT COALESCE(MAX(seq),-1)+1 FROM messages WHERE session_uuid=?), ?, 'tool_use', ?, '{}')`,
		sess, sess, ts, tool+" "+path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := d.Exec(`INSERT INTO tool_calls(message_id, tool_name, params_summary) VALUES (?,?,?)`, id, tool, path); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (?,?,?,'file',?)`, id, sess, ts, path); err != nil {
		t.Fatal(err)
	}
}

func TestFileHistoryOrderAndTools(t *testing.T) {
	d := testDB(t)
	const f = "/p/a.go"
	addSessionAt(t, d, "A", "/p", 3000, "")
	addSessionAt(t, d, "B", "/p", 2000, "")
	addSessionAt(t, d, "C", "/p", 1000, "")
	addTouch(t, d, "C", "Read", f, 100)
	addTouch(t, d, "C", "Edit", f, 110)
	addTouch(t, d, "B", "Read", f, 200)
	addTouch(t, d, "A", "Edit", f, 300)
	addTouch(t, d, "A", "Edit", "/p/other.go", 310) // another file: not in this history

	rows, newest, err := FileHistory(context.Background(), d, f, "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].SessionUUID != "A" || rows[1].SessionUUID != "B" || rows[2].SessionUUID != "C" {
		t.Fatalf("want A,B,C newest first, got %+v", rows)
	}
	if rows[0].TS != 300 || rows[0].Title != "title-A" {
		t.Errorf("row A = %+v", rows[0])
	}
	if strings.Join(rows[2].Tools, ",") != "Edit,Read" {
		t.Errorf("C tools should be the distinct sorted set, got %v", rows[2].Tools)
	}
	if newest != 300 {
		t.Errorf("newestTS=%d want 300", newest)
	}
}

func TestFileHistorySubagentRollsUpToParent(t *testing.T) {
	d := testDB(t)
	addSessionAt(t, d, "P", "/p", 1000, "")
	addSessionAt(t, d, "child", "/p", 1500, "P")
	addTouch(t, d, "child", "Edit", "/p/a.go", 1400)
	rows, _, err := FileHistory(context.Background(), d, "/p/a.go", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SessionUUID != "P" || rows[0].Title != "title-P" {
		t.Fatalf("child touch should roll up to parent P, got %+v", rows)
	}
}

func TestFileHistoryClaudeCodeOnly(t *testing.T) {
	d := testDB(t)
	addSessionAt(t, d, "cc", "/p", 1000, "")
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, ended_at, turn_count, title, source) VALUES ('cx','/p','cx.jsonl',2000,1,'codex one','codex')`); err != nil {
		t.Fatal(err)
	}
	addTouch(t, d, "cc", "Read", "/p/a.go", 900)
	addTouch(t, d, "cx", "Edit", "/p/a.go", 1900)
	rows, newest, err := FileHistory(context.Background(), d, "/p/a.go", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SessionUUID != "cc" || newest != 900 {
		t.Fatalf("codex rows must be excluded from rows and newestTS, got %+v newest=%d", rows, newest)
	}
}

func TestFileHistoryExcludeCallerSinceAndLimit(t *testing.T) {
	d := testDB(t)
	for i, u := range []string{"s1", "s2", "s3", "me"} {
		addSessionAt(t, d, u, "/p", int64(1000*(i+1)), "")
		addTouch(t, d, u, "Read", "/p/a.go", int64(1000*(i+1)-50))
	}
	// caller excluded from rows, not from newestTS
	rows, newest, err := FileHistory(context.Background(), d, "/p/a.go", "me", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].SessionUUID != "s3" {
		t.Fatalf("caller must be dropped from rows: %+v", rows)
	}
	if newest != 3950 {
		t.Errorf("newestTS must include the caller's touch: %d", newest)
	}
	// since bounds rows
	rows, _, err = FileHistory(context.Background(), d, "/p/a.go", "", 2500, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("since=2500 should keep s3 and me only, got %+v", rows)
	}
	// limit bounds rows but not newestTS
	rows, newest, err = FileHistory(context.Background(), d, "/p/a.go", "", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SessionUUID != "me" || newest != 3950 {
		t.Errorf("limit=1 → one row, newestTS unchanged: %+v newest=%d", rows, newest)
	}
}

// Excluding a parent drops its subagents' touches too; excluding a subagent
// drops only its own rows while the parent's other activity stays (codex review).
func TestFileHistoryExcludeCallerSubagentSemantics(t *testing.T) {
	d := testDB(t)
	addSessionAt(t, d, "P", "/p", 1000, "")
	addSessionAt(t, d, "C", "/p", 1500, "P")
	addSessionAt(t, d, "other", "/p", 500, "")
	addTouch(t, d, "P", "Read", "/p/a.go", 700)
	addTouch(t, d, "C", "Edit", "/p/a.go", 1400)
	addTouch(t, d, "other", "Read", "/p/a.go", 400)

	rows, newest, err := FileHistory(context.Background(), d, "/p/a.go", "P", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SessionUUID != "other" || newest != 1400 {
		t.Fatalf("excluding the parent must drop the child's touch too: %+v newest=%d", rows, newest)
	}

	rows, _, err = FileHistory(context.Background(), d, "/p/a.go", "C", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].SessionUUID != "P" || rows[0].TS != 700 || strings.Join(rows[0].Tools, ",") != "Read" {
		t.Fatalf("excluding the child must keep the parent's own Read only: %+v", rows)
	}
}

func TestFileHistoryIgnoresOrphanAndSortsUndatedLast(t *testing.T) {
	d := testDB(t)
	addSessionAt(t, d, "s", "/p", 1000, "")
	addSessionAt(t, d, "u", "/p", 900, "")
	addTouch(t, d, "s", "Read", "/p/a.go", 950)
	addTouch(t, d, "u", "Edit", "/p/a.go", nil) // undated fact
	// orphan: a fact whose session was deleted (no FK) — newer than anything
	if _, err := d.Exec(`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (999999,'gone',99999,'file','/p/a.go')`); err != nil {
		t.Fatal(err)
	}
	rows, newest, err := FileHistory(context.Background(), d, "/p/a.go", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].SessionUUID != "s" || rows[1].SessionUUID != "u" || rows[1].TS != 0 {
		t.Fatalf("orphan must not appear and undated sorts last: %+v", rows)
	}
	if newest != 950 {
		t.Errorf("orphan must not move newestTS: %d", newest)
	}
}

func TestFileHistoryNoRows(t *testing.T) {
	d := testDB(t)
	rows, newest, err := FileHistory(context.Background(), d, "/nope", "", 0, 10)
	if err != nil || len(rows) != 0 || newest != 0 {
		t.Fatalf("empty history: rows=%v newest=%d err=%v", rows, newest, err)
	}
}

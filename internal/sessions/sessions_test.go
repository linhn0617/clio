package sessions

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "s.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func addSession(t *testing.T, d *db.DB, uuid, project string, turns int) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title) VALUES (?,?,?,?,?,?,?)`,
		uuid, project, uuid+".jsonl", time.Now().Unix(), time.Now().Unix(), turns, "title-"+uuid); err != nil {
		t.Fatal(err)
	}
}

func addMsg(t *testing.T, d *db.DB, sess string, seq int, role, content string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES (?,?,?,?,?,?)`,
		sess, seq, time.Now().Unix(), role, content, "{}"); err != nil {
		t.Fatal(err)
	}
}

func TestResolvePrefixExact(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abcdef12-3456", "/p", 1)
	s, err := ResolvePrefix(d, "abcdef12-3456")
	if err != nil || s.UUID != "abcdef12-3456" {
		t.Fatalf("exact resolve failed: %v %+v", err, s)
	}
}

func TestResolvePrefixUnambiguous(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abcdef12-3456", "/p", 1)
	addSession(t, d, "ffffffff-0000", "/p", 1)
	s, err := ResolvePrefix(d, "abc")
	if err != nil || s.UUID != "abcdef12-3456" {
		t.Fatalf("prefix resolve failed: %v %+v", err, s)
	}
}

func TestResolvePrefixAmbiguous(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abc111", "/p", 1)
	addSession(t, d, "abc222", "/p", 1)
	if _, err := ResolvePrefix(d, "abc"); err != ErrAmbiguous {
		t.Fatalf("expected ErrAmbiguous, got %v", err)
	}
}

func TestResolvePrefixNotFound(t *testing.T) {
	d := testDB(t)
	if _, err := ResolvePrefix(d, "nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetMessagesPaginationAndHasMore(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 5)
	for i := range 5 {
		addMsg(t, d, "s1", i, "user", "m")
	}
	page, hasMore, err := GetMessages(d, "s1", 0, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || !hasMore {
		t.Fatalf("page=%d hasMore=%v want 2,true", len(page), hasMore)
	}
	last, hasMore, _ := GetMessages(d, "s1", 4, 2, false)
	if len(last) != 1 || hasMore {
		t.Fatalf("last page=%d hasMore=%v want 1,false", len(last), hasMore)
	}
}

func TestGetMessagesExcludesToolOutput(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addMsg(t, d, "s1", 0, "user", "hi")
	addMsg(t, d, "s1", 1, "tool_result", "tool noise")
	page, _, _ := GetMessages(d, "s1", 0, 50, false)
	if len(page) != 1 || page[0].Role != "user" {
		t.Fatalf("expected only user msg, got %+v", page)
	}
	all, _, _ := GetMessages(d, "s1", 0, 50, true)
	if len(all) != 2 {
		t.Fatalf("with tool output expected 2, got %d", len(all))
	}
}

func TestListSessionsMinTurns(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addSession(t, d, "s2", "/p", 10)
	rows, err := ListSessions(d, ListFilter{MinTurns: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UUID != "s2" {
		t.Fatalf("min-turns filter failed: %+v", rows)
	}
}

func TestActivitySummaryGrouping(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/a", 1)
	addMsg(t, d, "s1", 0, "user", "x")
	since := time.Now().Add(-24 * time.Hour).Unix()
	if _, err := ActivitySummary(d, since, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivitySummary(d, since, "day"); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivitySummary(d, since, "bogus"); err == nil {
		t.Fatal("expected error for invalid group_by")
	}
}

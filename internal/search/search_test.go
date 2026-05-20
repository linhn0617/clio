package search

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

func addSession(t *testing.T, d *db.DB, uuid, project string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, turn_count) VALUES (?,?,?,0)`,
		uuid, project, uuid+".jsonl"); err != nil {
		t.Fatal(err)
	}
}

func addMsg(t *testing.T, d *db.DB, sess string, seq int, role, content string, ts int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES (?,?,?,?,?,?)`,
		sess, seq, ts, role, content, "{}"); err != nil {
		t.Fatal(err)
	}
}

func TestSearchEmptyQueryErrors(t *testing.T) {
	d := testDB(t)
	if _, err := Search(d, Options{Query: "  "}); err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestSearchFTS3Char(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "請幫我設計資料庫遷移流程", now)
	addMsg(t, d, "s1", 1, "assistant", "unrelated english text", now)

	res, err := Search(d, Options{Query: "資料庫", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].SessionUUID != "s1" {
		t.Fatalf("expected 1 FTS hit, got %+v", res)
	}
}

func TestSearchCJKShortFallback(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "資料驗證流程很重要", now)

	// 2-char query: trigram can't match, LIKE fallback must.
	res, err := Search(d, Options{Query: "驗證", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected LIKE fallback to find 1, got %d", len(res))
	}
}

func TestSearchDialogueOutranksToolOutput(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "how do we handle authentication here", now)
	addMsg(t, d, "s1", 1, "tool_result", "authentication authentication authentication log noise", now)

	res, err := Search(d, Options{Query: "authentication", Limit: 10, IncludeToolOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) < 2 {
		t.Fatalf("expected both rows, got %d", len(res))
	}
	if res[0].Role != "user" {
		t.Fatalf("expected user message ranked first, got %q", res[0].Role)
	}
}

func TestSearchExcludesToolOutputByDefault(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "tool_result", "matched only in tool output zzztoken", now)

	res, err := Search(d, Options{Query: "zzztoken", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("tool output should be excluded by default, got %d", len(res))
	}
	res, err = Search(d, Options{Query: "zzztoken", Limit: 10, IncludeToolOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("with include-tool-output expected 1, got %d", len(res))
	}
}

func TestSearchProjectFilter(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/alpha")
	addSession(t, d, "s2", "/p/beta")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "shared keyword qubit", now)
	addMsg(t, d, "s2", 0, "user", "shared keyword qubit", now)

	res, err := Search(d, Options{Query: "qubit", ProjectPrefix: "/p/alpha", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].SessionUUID != "s1" {
		t.Fatalf("project filter failed: %+v", res)
	}
}

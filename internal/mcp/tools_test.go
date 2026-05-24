package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/linhn0617/clio/internal/db"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "m.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func addSession(t *testing.T, d *db.DB, uuid, project string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, ended_at, turn_count) VALUES (?,?,?,?,1)`,
		uuid, project, uuid+".jsonl", time.Now().Unix()); err != nil {
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

func call(t *testing.T, h func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := h(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return res
}

func resultJSON(t *testing.T, r *mcp.CallToolResult) map[string]any {
	t.Helper()
	if len(r.Content) == 0 {
		t.Fatal("no content")
	}
	tc, ok := r.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content not text: %T", r.Content[0])
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &m); err != nil {
		t.Fatalf("result not JSON: %v\n%s", err, tc.Text)
	}
	return m
}

func TestClamp(t *testing.T) {
	cases := []struct{ v, def, max, want int }{
		{0, 10, 50, 10},
		{-5, 10, 50, 10},
		{999, 10, 50, 50},
		{20, 10, 50, 20},
	}
	for _, c := range cases {
		if got := clamp(c.v, c.def, c.max); got != c.want {
			t.Errorf("clamp(%d,%d,%d)=%d want %d", c.v, c.def, c.max, got, c.want)
		}
	}
}

func TestParseSince(t *testing.T) {
	now := time.Now().Unix()
	if ts := parseSince("1d"); ts > now-86000 || ts < now-87000 {
		t.Errorf("1d => %d (now %d) out of expected range", ts, now)
	}
	if ts := parseSince(""); ts != 0 {
		t.Errorf("empty => %d want 0", ts)
	}
	if ts := parseSince("2026-01-01"); ts == 0 {
		t.Error("absolute date should parse")
	}
}

func TestHandleSearchRequiresQuery(t *testing.T) {
	d := testDB(t)
	r := call(t, handleSearch(d, nil), map[string]any{})
	if !r.IsError {
		t.Fatal("expected error result when query missing")
	}
}

func TestHandleSearchReturnsHits(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/x")
	addMsg(t, d, "s1", 0, "user", "design the authentication module")
	r := call(t, handleSearch(d, nil), map[string]any{"query": "authentication"})
	m := resultJSON(t, r)
	if int(m["count"].(float64)) != 1 {
		t.Fatalf("count=%v want 1", m["count"])
	}
}

func TestHandleReadSessionPagination(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/x")
	for i := range 5 {
		addMsg(t, d, "s1", i, "user", "message")
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "s1", "limit": 2, "offset": 0})
	m := resultJSON(t, r)
	if m["has_more"] != true {
		t.Fatalf("expected has_more=true, got %v", m["has_more"])
	}
	if len(m["messages"].([]any)) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(m["messages"].([]any)))
	}

	r2 := call(t, handleReadSession(d, nil), map[string]any{"uuid": "s1", "limit": 2, "offset": 4})
	m2 := resultJSON(t, r2)
	if m2["has_more"] != false {
		t.Fatalf("expected has_more=false at end, got %v", m2["has_more"])
	}
}

func TestHandleReadSessionUnknownUUID(t *testing.T) {
	d := testDB(t)
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "does-not-exist"})
	if !r.IsError {
		t.Fatal("expected error for unknown uuid")
	}
}

func TestHandleActivitySummary(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/x")
	addMsg(t, d, "s1", 0, "user", "hi")
	r := call(t, handleActivitySummary(d, nil), map[string]any{"since": "7d", "group_by": "project"})
	m := resultJSON(t, r)
	if _, ok := m["buckets"]; !ok {
		t.Fatal("expected buckets in summary")
	}
}

func TestHandleActivitySummaryRejectsBadGroupBy(t *testing.T) {
	d := testDB(t)
	r := call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "week"})
	if !r.IsError {
		t.Fatal("expected an error result for an unsupported group_by")
	}
}

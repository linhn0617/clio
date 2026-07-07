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

func addTarget(t *testing.T, d *db.DB, sess, kind, value string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (0,?,?,?,?)`,
		sess, time.Now().Unix(), kind, value); err != nil {
		t.Fatal(err)
	}
}

func TestHandleListSessionsFilterByTool(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addSession(t, d, "s2", "/p")
	addTarget(t, d, "s1", "tool", "Bash")
	r := call(t, handleListSessions(d, nil), map[string]any{"tool": "Bash"})
	m := resultJSON(t, r)
	if int(m["count"].(float64)) != 1 {
		t.Fatalf("count=%v want 1 (only s1 used Bash)", m["count"])
	}
}

func TestHandleListSessionsFilterByTouchedAndRan(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addSession(t, d, "s2", "/p")
	addTarget(t, d, "s1", "file", "/x/auth.ts")
	addTarget(t, d, "s2", "command", "go test ./...")

	r := call(t, handleListSessions(d, nil), map[string]any{"touched": "/x/auth"})
	if got := int(resultJSON(t, r)["count"].(float64)); got != 1 {
		t.Fatalf("touched filter count=%d want 1", got)
	}
	r2 := call(t, handleListSessions(d, nil), map[string]any{"ran": "go test"})
	if got := int(resultJSON(t, r2)["count"].(float64)); got != 1 {
		t.Fatalf("ran filter count=%d want 1 (only s2 ran it)", got)
	}
}

func TestHandleActivitySummaryByTool(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addTarget(t, d, "s1", "tool", "Bash")
	addTarget(t, d, "s1", "tool", "Bash")
	addTarget(t, d, "s1", "tool", "Edit")
	r := call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "tool"})
	m := resultJSON(t, r)
	acts, ok := m["activity"].([]any)
	if !ok || len(acts) != 2 {
		t.Fatalf("expected 2 activity rows, got %v", m["activity"])
	}
	top := acts[0].(map[string]any)
	if top["value"] != "Bash" || int(top["count"].(float64)) != 2 {
		t.Fatalf("top activity = %v, want Bash count 2", top)
	}
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

// The MCP tool declares a `project` param (server.go) but ActivitySummary used to
// ignore it for day/project grouping, silently returning all-project data. Verify
// the filter actually scopes the result for both groupings.
func TestHandleActivitySummaryFiltersByProject(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/a")
	addSession(t, d, "s2", "/p/b")
	addMsg(t, d, "s1", 0, "user", "x")
	addMsg(t, d, "s2", 0, "user", "y")

	rProject := call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "project", "project": "/p/a"})
	mProject := resultJSON(t, rProject)
	bucketsProject := mProject["buckets"].([]any)
	if len(bucketsProject) != 1 {
		t.Fatalf("group_by=project with project filter should return 1 bucket, got %+v", bucketsProject)
	}
	if key := bucketsProject[0].(map[string]any)["Key"]; key != "/p/a" {
		t.Fatalf("bucket key = %v, want /p/a", key)
	}

	rDay := call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "day", "project": "/p/a"})
	mDay := resultJSON(t, rDay)
	bucketsDay := mDay["buckets"].([]any)
	total := 0
	for _, b := range bucketsDay {
		total += int(b.(map[string]any)["Sessions"].(float64))
	}
	if total != 1 {
		t.Fatalf("group_by=day with project filter should count 1 session total, got %d across %+v", total, bucketsDay)
	}
}

func TestHandleActivitySummaryRejectsBadGroupBy(t *testing.T) {
	d := testDB(t)
	r := call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "week"})
	if !r.IsError {
		t.Fatal("expected an error result for an unsupported group_by")
	}
}

func addChild(t *testing.T, d *db.DB, uuid, project, parent, agentType string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, ended_at, turn_count, parent_session, agent_type) VALUES (?,?,?,?,1,?,?)`,
		uuid, project, uuid+".jsonl", time.Now().Unix(), parent, agentType); err != nil {
		t.Fatal(err)
	}
}

func TestHandleListSessionsNestsSubagents(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addChild(t, d, "agent-c", "/p", "P", "general-purpose")

	// Default: only the top-level parent, annotated with its subagent count.
	def := resultJSON(t, call(t, handleListSessions(d, nil), map[string]any{}))
	if int(def["count"].(float64)) != 1 {
		t.Fatalf("default should list only the parent, count=%v", def["count"])
	}
	prow := def["sessions"].([]any)[0].(map[string]any)
	if prow["subagent_count"] == nil || int(prow["subagent_count"].(float64)) != 1 {
		t.Fatalf("parent should report subagent_count=1, got %v", prow["subagent_count"])
	}

	// include_subagents: both rows; the child carries its parent link and type.
	all := resultJSON(t, call(t, handleListSessions(d, nil), map[string]any{"include_subagents": true}))
	if int(all["count"].(float64)) != 2 {
		t.Fatalf("include_subagents should list both, count=%v", all["count"])
	}
	var child map[string]any
	for _, s := range all["sessions"].([]any) {
		if row := s.(map[string]any); row["uuid"] == "agent-c" {
			child = row
		}
	}
	if child == nil || child["parent_session"] != "P" || child["agent_type"] != "general-purpose" {
		t.Fatalf("child row missing parent/type: %v", child)
	}
}

func TestHandleSearchCarriesSubagentInfo(t *testing.T) {
	d := testDB(t)
	addChild(t, d, "agent-z", "/p", "parent-z", "general-purpose")
	addMsg(t, d, "agent-z", 0, "assistant", "subagentfinding alpha")
	m := resultJSON(t, call(t, handleSearch(d, nil), map[string]any{"query": "subagentfinding"}))
	hits := m["results"].([]any)
	if len(hits) == 0 {
		t.Fatal("expected a hit from the subagent session")
	}
	h := hits[0].(map[string]any)
	if h["parent_session"] != "parent-z" || h["agent_type"] != "general-purpose" {
		t.Fatalf("hit missing parent/type: %v", h)
	}
}

func TestHandleReadSessionReportsSubagents(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addChild(t, d, "agent-c", "/p", "P", "general-purpose")
	addMsg(t, d, "P", 0, "user", "hi")
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P"}))
	subs, ok := m["subagents"].([]any)
	if !ok || len(subs) != 1 {
		t.Fatalf("expected 1 subagent reported, got %v", m["subagents"])
	}
	sub := subs[0].(map[string]any)
	if sub["uuid"] != "agent-c" || sub["agent_type"] != "general-purpose" {
		t.Fatalf("subagent should carry uuid + type: %v", sub)
	}
	// Reading the subagent itself surfaces its parent link and type at session level.
	mc := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "agent-c"}))
	sess := mc["session"].(map[string]any)
	if sess["parent_session"] != "P" || sess["agent_type"] != "general-purpose" {
		t.Fatalf("a subagent's session block should carry parent_session + agent_type: %v", sess)
	}
}

// read_session inlines each subagent's messages when include_subagents is set,
// matching CLI `show --include-subagents`.
func TestHandleReadSessionInlinesSubagents(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addChild(t, d, "agent-c", "/p", "P", "general-purpose")
	addMsg(t, d, "agent-c", 0, "assistant", "SUBMSG")
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "include_subagents": true}))
	subs := m["subagents"].([]any)
	if len(subs) != 1 {
		t.Fatalf("expected 1 subagent, got %v", m["subagents"])
	}
	msgs, ok := subs[0].(map[string]any)["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("include_subagents should inline the child's messages, got %v", subs[0])
	}
	if msgs[0].(map[string]any)["content"] != "SUBMSG" {
		t.Fatalf("inlined child message content wrong: %v", msgs[0])
	}
}

// An inlined subagent transcript longer than the page limit signals has_more, so a
// client can tell it was truncated (and paginate the child via read_session).
func TestHandleReadSessionInlineSignalsTruncation(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addChild(t, d, "agent-c", "/p", "P", "general-purpose")
	for i := range 5 {
		addMsg(t, d, "agent-c", i, "user", "m")
	}
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "include_subagents": true, "limit": 2}))
	sub := m["subagents"].([]any)[0].(map[string]any)
	if msgs := sub["messages"].([]any); len(msgs) != 2 {
		t.Fatalf("child page should respect limit=2, got %d", len(msgs))
	}
	if sub["has_more"] != true {
		t.Fatalf("a truncated inlined child transcript must signal has_more, got %v", sub["has_more"])
	}
}

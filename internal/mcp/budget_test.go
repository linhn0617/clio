package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/linhn0617/clio/internal/db"
)

// resultLen measures what the client actually receives: every text content in
// Unicode code points, plus structuredContent if the handler attached one
// (a second copy of the payload, which the budget must also cover).
func resultLen(t *testing.T, r *mcp.CallToolResult) int {
	t.Helper()
	n := 0
	for _, c := range r.Content {
		tc, ok := c.(mcp.TextContent)
		if !ok {
			t.Fatalf("content not text: %T", c)
		}
		n += utf8.RuneCountInString(tc.Text)
	}
	if r.StructuredContent != nil {
		b, err := json.Marshal(r.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		n += utf8.RuneCount(b)
	}
	return n
}

func wantTruncated(t *testing.T, r *mcp.CallToolResult, budget int) map[string]any {
	t.Helper()
	m := resultJSON(t, r)
	if m["truncated"] != true {
		t.Fatalf("expected truncated=true, got %v (len=%d)", m["truncated"], resultLen(t, r))
	}
	if s, _ := m["next"].(string); s == "" {
		t.Fatalf("expected a non-empty next hint, got %v", m["next"])
	}
	if n := resultLen(t, r); n > budget {
		t.Fatalf("serialized response is %d chars, over the %d budget", n, budget)
	}
	return m
}

func TestBudgetUnderBudgetHasNoTruncatedFlag(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	r := call(t, handleListSessions(d, nil), map[string]any{})
	m := resultJSON(t, r)
	if _, has := m["truncated"]; has {
		t.Fatalf("a small response must not carry truncated: %v", m)
	}
	if _, has := m["next"]; has {
		t.Fatalf("a small response must not carry next: %v", m)
	}
}

func TestBudgetListSessionsTruncatesAtRowBoundary(t *testing.T) {
	d := testDB(t)
	for i := range 50 {
		addSession(t, d, fmt.Sprintf("s%02d", i), "/p/"+strings.Repeat("x", 200))
	}
	r := call(t, handleListSessions(d, nil), map[string]any{"limit": 50, "max_chars": 4000})
	m := wantTruncated(t, r, 4000)
	rows := m["sessions"].([]any)
	if int(m["count"].(float64)) != len(rows) || len(rows) == 0 || len(rows) >= 50 {
		t.Fatalf("count must equal the kept rows (0 < n < 50): count=%v rows=%d", m["count"], len(rows))
	}
}

func TestBudgetSearchTruncates(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/"+strings.Repeat("x", 200))
	for i := range 50 {
		addMsg(t, d, "s1", i, "user", "needlehaystack "+strings.Repeat("filler ", 40))
	}
	r := call(t, handleSearch(d, nil), map[string]any{"query": "needlehaystack", "limit": 50, "max_chars": 4000})
	m := wantTruncated(t, r, 4000)
	if n := len(m["results"].([]any)); n == 0 || n >= 50 {
		t.Fatalf("expected a partial result list, got %d", n)
	}
}

func TestBudgetActivitySummaryTruncates(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	for i := range 50 {
		addTarget(t, d, "s1", "command", fmt.Sprintf("cmd-%02d-%s", i, strings.Repeat("y", 150)))
	}
	r := call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "command", "limit": 50, "max_chars": 4000})
	m := wantTruncated(t, r, 4000)
	if n := len(m["activity"].([]any)); n == 0 || n >= 50 {
		t.Fatalf("expected a partial activity list, got %d", n)
	}
}

func TestBudgetActivitySummaryDayBucketsTruncate(t *testing.T) {
	d := testDB(t)
	for i := range 60 {
		addSession(t, d, fmt.Sprintf("s%02d", i), fmt.Sprintf("/p/%02d/%s", i, strings.Repeat("z", 200)))
	}
	r := call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "project", "max_chars": 4000})
	m := wantTruncated(t, r, 4000)
	if n := len(m["buckets"].([]any)); n == 0 || n >= 60 {
		t.Fatalf("expected a partial bucket list, got %d", n)
	}
}

func TestBudgetAskTruncates(t *testing.T) {
	d := testDB(t)
	for i := range 6 {
		id := fmt.Sprintf("s%d", i)
		addSession(t, d, id, "/p")
		addMsg(t, d, id, 0, "user", "authentication failure "+strings.Repeat("detail ", 300))
	}
	r := call(t, handleAsk(d, nil), map[string]any{"question": "authentication failure", "max_tokens": 8000, "max_chars": 4000})
	m := wantTruncated(t, r, 4000)
	if len(m["groups"].([]any)) == 0 {
		t.Fatal("at least one group must be kept")
	}
}

func TestBudgetReadSessionTruncatesWithNextOffset(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 20 {
		addMsg(t, d, "P", i, "user", strings.Repeat("m", 800))
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 4000})
	m := wantTruncated(t, r, 4000)
	kept := len(m["messages"].([]any))
	if kept == 0 || kept >= 20 {
		t.Fatalf("expected a partial page, got %d", kept)
	}
	if int(m["next_offset"].(float64)) != kept {
		t.Fatalf("next_offset=%v want %d", m["next_offset"], kept)
	}
	if m["has_more"] != true {
		t.Fatalf("a truncated page has more rows: has_more=%v", m["has_more"])
	}
	// Following next_offset yields the very next message.
	m2 := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "offset": kept, "max_chars": 4000}))
	if first := m2["messages"].([]any)[0].(map[string]any); int(first["seq"].(float64)) != kept {
		t.Fatalf("next page should start at seq %d, got %v", kept, first["seq"])
	}
}

func TestBudgetSingleOversizedRowStillReturned(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addMsg(t, d, "P", 0, "user", strings.Repeat("a", 6000))
	addMsg(t, d, "P", 1, "user", strings.Repeat("b", 6000))
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 4000})
	m := resultJSON(t, r)
	if n := len(m["messages"].([]any)); n != 1 {
		t.Fatalf("one oversized row must still be returned, got %d", n)
	}
	if m["truncated"] != true {
		t.Fatalf("truncated must be true, got %v", m["truncated"])
	}
	// A lone oversized row (nothing dropped) is still flagged.
	d2 := testDB(t)
	addSession(t, d2, "Q", "/p")
	addMsg(t, d2, "Q", 0, "user", strings.Repeat("a", 6000))
	m2 := resultJSON(t, call(t, handleReadSession(d2, nil), map[string]any{"uuid": "Q", "max_chars": 4000}))
	if len(m2["messages"].([]any)) != 1 || m2["truncated"] != true {
		t.Fatalf("lone oversized row: want 1 message + truncated, got %v", m2)
	}
}

func TestBudgetMaxCharsIsClamped(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 20 {
		addMsg(t, d, "P", i, "user", strings.Repeat("m", 800))
	}
	// Below the floor: clamped up to 4000, not honoured literally.
	lo := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 10})
	if n := resultLen(t, lo); n < 3000 || n > 4000 {
		t.Fatalf("max_chars=10 should clamp to 4000 (len in 3000..4000), got %d", n)
	}
	// Above the ceiling: clamped down to 48000. 100 x 800 = 80k exceeds it
	// even with an absurd max_chars.
	d2 := testDB(t)
	addSession(t, d2, "P", "/p")
	for i := range 100 {
		addMsg(t, d2, "P", i, "user", strings.Repeat("m", 800))
	}
	r := call(t, handleReadSession(d2, nil), map[string]any{"uuid": "P", "limit": 200, "max_chars": 10_000_000})
	wantTruncated(t, r, 48000)
}

func TestBudgetDefaultIsBelowClaudeCodeThreshold(t *testing.T) {
	if defaultMaxChars >= 50000 || defaultMaxChars != 40000 {
		t.Fatalf("defaultMaxChars=%d, want 40000", defaultMaxChars)
	}
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 200 {
		addMsg(t, d, "P", i, "user", strings.Repeat("m", 800))
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "limit": 200})
	wantTruncated(t, r, 40000)
}

func seedSeq(t *testing.T, d *db.DB, n int) {
	t.Helper()
	addSession(t, d, "P", "/p")
	for i := range n {
		addMsg(t, d, "P", i, "user", fmt.Sprintf("msg-%d", i))
	}
}

func seqs(t *testing.T, m map[string]any) []int {
	t.Helper()
	var out []int
	for _, x := range m["messages"].([]any) {
		out = append(out, int(x.(map[string]any)["seq"].(float64)))
	}
	return out
}

func wantSeqs(t *testing.T, got []int, from, to int) {
	t.Helper()
	if len(got) != to-from+1 {
		t.Fatalf("got seqs %v, want %d..%d", got, from, to)
	}
	for i, s := range got {
		if s != from+i {
			t.Fatalf("got seqs %v, want %d..%d", got, from, to)
		}
	}
}

func TestReadSessionLastN(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 30)
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "last_n": 5}))
	wantSeqs(t, seqs(t, m), 25, 29)
	if int(m["offset"].(float64)) != 25 || int(m["total"].(float64)) != 30 || m["has_more"] != false {
		t.Fatalf("pagination info wrong: offset=%v total=%v has_more=%v", m["offset"], m["total"], m["has_more"])
	}
	// last_n larger than the session: everything, from offset 0.
	m = resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "last_n": 99}))
	wantSeqs(t, seqs(t, m), 0, 29)
	if int(m["offset"].(float64)) != 0 {
		t.Fatalf("offset=%v want 0", m["offset"])
	}
}

func TestReadSessionLastNSkipsToolOutputByDefault(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addMsg(t, d, "P", 0, "user", "u0")
	addMsg(t, d, "P", 1, "tool_result", "t1")
	addMsg(t, d, "P", 2, "assistant", "a2")
	addMsg(t, d, "P", 3, "tool_result", "t3")
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "last_n": 2}))
	got := seqs(t, m)
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("last_n counts dialogue messages only by default: got %v", got)
	}
	if int(m["total"].(float64)) != 2 {
		t.Fatalf("total=%v want 2 dialogue messages", m["total"])
	}
}

func TestReadSessionAroundSequence(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 100)
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": 50, "limit": 10}))
	wantSeqs(t, seqs(t, m), 45, 54)
	if int(m["offset"].(float64)) != 45 || int(m["total"].(float64)) != 100 || m["has_more"] != true {
		t.Fatalf("pagination info wrong: offset=%v total=%v has_more=%v", m["offset"], m["total"], m["has_more"])
	}
	// Near the start: window is clamped at offset 0.
	m = resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": 2, "limit": 10}))
	wantSeqs(t, seqs(t, m), 0, 9)
	// Near the end: window ends at the last message, has_more false.
	m = resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": 99, "limit": 10}))
	if got := seqs(t, m); got[len(got)-1] != 99 || m["has_more"] != false {
		t.Fatalf("tail window: seqs=%v has_more=%v", got, m["has_more"])
	}
	// around_sequence 0 is a valid value, not "unset".
	m = resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": 0, "limit": 10}))
	wantSeqs(t, seqs(t, m), 0, 9)
}

func TestReadSessionModesAreMutuallyExclusive(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 10)
	for _, args := range []map[string]any{
		{"uuid": "P", "offset": 2, "last_n": 3},
		{"uuid": "P", "offset": 2, "around_sequence": 3},
		{"uuid": "P", "last_n": 3, "around_sequence": 3},
		{"uuid": "P", "offset": 1, "last_n": 3, "around_sequence": 3},
	} {
		r := call(t, handleReadSession(d, nil), args)
		if !r.IsError {
			t.Fatalf("%v: expected a tool error", args)
		}
		txt := r.Content[0].(mcp.TextContent).Text
		for _, name := range []string{"offset", "last_n", "around_sequence"} {
			if !strings.Contains(txt, name) {
				t.Fatalf("%v: error should name %s, got %q", args, name, txt)
			}
		}
	}
	if r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "last_n": 3}); r.IsError {
		t.Fatal("last_n alone must work")
	}
}

func TestReadSessionPlainOffsetStillReportsTotal(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 12)
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "offset": 4, "limit": 3}))
	wantSeqs(t, seqs(t, m), 4, 6)
	if int(m["offset"].(float64)) != 4 || m["has_more"] != true {
		t.Fatalf("offset=%v has_more=%v", m["offset"], m["has_more"])
	}
}

// Clients may echo the schema default offset=0 back; that must not count as a
// second mode next to last_n.
func TestReadSessionExplicitZeroOffsetIsNotAMode(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 10)
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "offset": 0, "last_n": 3})
	if r.IsError {
		t.Fatalf("offset=0 with last_n must work: %v", r.Content)
	}
	wantSeqs(t, seqs(t, resultJSON(t, r)), 7, 9)
}

// When even one parent message plus inlined subagent transcripts overflow, the
// subagent messages are dropped (still listed, has_more) so the budget holds.
func TestBudgetReadSessionDropsInlinedSubagentMessagesAsLastResort(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addMsg(t, d, "P", 0, "user", "hello")
	addChild(t, d, "agent-c", "/p", "P", "general-purpose")
	for i := range 20 {
		addMsg(t, d, "agent-c", i, "assistant", strings.Repeat("s", 800))
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "include_subagents": true, "max_chars": 4000})
	if n := resultLen(t, r); n > 4000 {
		t.Fatalf("response %d chars over budget", n)
	}
	m := resultJSON(t, r)
	sub := m["subagents"].([]any)[0].(map[string]any)
	if sub["uuid"] != "agent-c" || sub["messages"] != nil || sub["has_more"] != true {
		t.Fatalf("subagent should be listed without messages and has_more=true: %v", sub)
	}
}

// ---- review round 2 ----

// bigDataset seeds enough data that every tool's full response exceeds 48,000
// characters, so each budget below must truncate.
func bigDataset(t *testing.T, d *db.DB) {
	t.Helper()
	long := strings.Repeat("p", 5000)
	for i := range 50 {
		id := fmt.Sprintf("s%02d", i)
		addSession(t, d, id, fmt.Sprintf("/%s/%02d", long, i))
		addMsg(t, d, id, 0, "user", "needlehaystack authentication failure "+strings.Repeat("filler ", 30))
		addTarget(t, d, id, "command", fmt.Sprintf("cmd-%02d-%s", i, long))
	}
	addSession(t, d, "R", "/r")
	for i := range 200 {
		addMsg(t, d, "R", i, "user", strings.Repeat("m", 800))
	}
}

func TestBudgetFinalResultWithinMaxCharsForEveryTool(t *testing.T) {
	d := testDB(t)
	bigDataset(t, d)
	tools := map[string]func(max int) *mcp.CallToolResult{
		"search": func(max int) *mcp.CallToolResult {
			return call(t, handleSearch(d, nil), map[string]any{"query": "needlehaystack", "limit": 50, "max_chars": max})
		},
		"list_sessions": func(max int) *mcp.CallToolResult {
			return call(t, handleListSessions(d, nil), map[string]any{"limit": 50, "max_chars": max})
		},
		"activity_summary": func(max int) *mcp.CallToolResult {
			return call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "command", "limit": 50, "max_chars": max})
		},
		"ask": func(max int) *mcp.CallToolResult {
			return call(t, handleAsk(d, nil), map[string]any{"question": "authentication failure", "limit": 10, "max_tokens": 8000, "max_chars": max})
		},
		"read_session": func(max int) *mcp.CallToolResult {
			return call(t, handleReadSession(d, nil), map[string]any{"uuid": "R", "limit": 200, "max_chars": max})
		},
	}
	for name, run := range tools {
		for _, max := range []int{4000, 40000, 48000} {
			r := run(max)
			if r.StructuredContent != nil {
				t.Errorf("%s/%d: structuredContent duplicates the payload", name, max)
			}
			next, _ := resultJSON(t, r)["next"].(string)
			if n := resultLen(t, r); n > max && !strings.Contains(next, "fixed fields exceed") {
				t.Errorf("%s/%d: final result is %d chars, over budget", name, max, n)
			}
			if resultJSON(t, r)["truncated"] != true {
				t.Errorf("%s/%d: dataset should exceed the budget and be truncated", name, max)
			}
		}
	}
}

func TestBudgetBudgetCountsUnicodeCodePointsNotBytes(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 20 {
		addMsg(t, d, "P", i, "user", strings.Repeat("字", 600))
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 4000})
	m := resultJSON(t, r)
	if n := resultLen(t, r); n > 4000 {
		t.Fatalf("%d code points over budget", n)
	}
	// 600 CJK runes per message: ~5 fit by runes; a byte count would allow only ~1.
	if kept := len(m["messages"].([]any)); kept < 4 {
		t.Fatalf("budget must count code points, not bytes: kept only %d", kept)
	}
}

func TestBudgetHugeSingleMessageIsCutByContent(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addMsg(t, d, "P", 0, "user", strings.Repeat("x", 120000))
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P"})
	if n := resultLen(t, r); n > defaultMaxChars {
		t.Fatalf("120k message must be cut inside the %d budget, got %d", defaultMaxChars, n)
	}
	m := resultJSON(t, r)
	if m["truncated"] != true {
		t.Fatalf("truncated=%v", m["truncated"])
	}
	c := m["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(c, "[truncated ") || !strings.HasPrefix(c, "xxxx") {
		t.Fatalf("content should keep a prefix and end with a truncation marker, got %q...", c[:40])
	}
}

func TestBudgetFixedFieldsExceedIsReportedAsSuch(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/"+strings.Repeat("x", 6000))
	r := call(t, handleListSessions(d, nil), map[string]any{"max_chars": 4000})
	m := resultJSON(t, r)
	if m["truncated"] != true || !strings.Contains(m["next"].(string), "fixed fields exceed max_chars") {
		t.Fatalf("want truncated + fixed-fields hint, got truncated=%v next=%v", m["truncated"], m["next"])
	}
	if len(m["sessions"].([]any)) != 1 {
		t.Fatal("the row is still returned")
	}
}

func TestBudgetSubagentsAreShedBeforeParentMessages(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 3 {
		addMsg(t, d, "P", i, "user", "parent-"+fmt.Sprint(i))
	}
	addChild(t, d, "agent-c", "/p", "P", "general-purpose")
	for i := range 20 {
		addMsg(t, d, "agent-c", i, "assistant", strings.Repeat("s", 800))
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "include_subagents": true, "max_chars": 4000})
	if n := resultLen(t, r); n > 4000 {
		t.Fatalf("response %d chars over budget", n)
	}
	m := resultJSON(t, r)
	if len(m["messages"].([]any)) != 3 {
		t.Fatalf("all parent messages must survive while subagent messages are shed: %v", m["messages"])
	}
	if m["truncated"] != true || !strings.Contains(m["next"].(string), "subagent") {
		t.Fatalf("omitting subagent messages must set truncated and explain: truncated=%v next=%v", m["truncated"], m["next"])
	}
}

func TestBudgetSubagentListIsTruncatable(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addMsg(t, d, "P", 0, "user", "hi")
	for i := range 200 {
		if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, ended_at, turn_count, parent_session, agent_type, title) VALUES (?,?,?,?,1,?,?,?)`,
			fmt.Sprintf("agent-%03d", i), "/p", "f", 1, "P", "general-purpose", strings.Repeat("t", 300)); err != nil {
			t.Fatal(err)
		}
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 4000})
	if n := resultLen(t, r); n > 4000 {
		t.Fatalf("response %d chars over budget", n)
	}
	m := resultJSON(t, r)
	if m["truncated"] != true {
		t.Fatalf("truncated=%v", m["truncated"])
	}
	if got := len(m["subagents"].([]any)); got == 0 || got >= 200 {
		t.Fatalf("subagent list should be a non-empty prefix, got %d", got)
	}
}

func TestBudgetAskKeepsHitsUnderEscapeHeavyData(t *testing.T) {
	d := testDB(t)
	for i := range 6 {
		id := fmt.Sprintf("s%d", i)
		addSession(t, d, id, "/p")
		addMsg(t, d, id, 0, "user", strings.Repeat("ctx \"q\" \\ \n", 200))
		addMsg(t, d, id, 1, "assistant", "authentication failure "+strings.Repeat("\"\\\n", 20))
		addMsg(t, d, id, 2, "user", strings.Repeat("ctx \"q\" \\ \n", 200))
	}
	r := call(t, handleAsk(d, nil), map[string]any{"question": "authentication failure", "max_chars": 4000})
	if n := resultLen(t, r); n > 4000 {
		t.Fatalf("response %d chars over budget", n)
	}
	m := resultJSON(t, r)
	groups := m["groups"].([]any)
	if len(groups) == 0 {
		t.Fatal("expected at least one group")
	}
	for _, g := range groups {
		hit := false
		for _, e := range g.(map[string]any)["excerpts"].([]any) {
			if e.(map[string]any)["is_hit"] == true {
				hit = true
			}
		}
		if !hit {
			t.Fatalf("every kept group must contain a hit excerpt: %v", g)
		}
	}
}

func TestBudgetReadSessionAroundKeepsTargetWhenTruncated(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 100 {
		addMsg(t, d, "P", i, "user", strings.Repeat("m", 800))
	}
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": 50, "limit": 40, "max_chars": 4000}))
	got := seqs(t, m)
	if m["truncated"] != true || len(got) >= 40 {
		t.Fatalf("expected a truncated window, got %v truncated=%v", got, m["truncated"])
	}
	found := false
	for _, s := range got {
		if s == 50 {
			found = true
		}
	}
	if !found {
		t.Fatalf("target seq 50 must survive truncation, got %v", got)
	}
	if int(m["offset"].(float64)) != got[0] {
		t.Fatalf("offset=%v must be the first returned seq %d", m["offset"], got[0])
	}
}

func TestBudgetReadSessionLastNKeepsTailWhenTruncated(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 100 {
		addMsg(t, d, "P", i, "user", strings.Repeat("m", 800))
	}
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "last_n": 50, "max_chars": 4000}))
	got := seqs(t, m)
	if m["truncated"] != true || got[len(got)-1] != 99 {
		t.Fatalf("the end of the session must survive truncation: %v truncated=%v", got, m["truncated"])
	}
	if int(m["offset"].(float64)) != got[0] {
		t.Fatalf("offset=%v must be the first returned seq %d", m["offset"], got[0])
	}
}

func TestReadSessionAroundNearEndBackfillsFullPage(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 100)
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": 98, "limit": 10}))
	wantSeqs(t, seqs(t, m), 90, 99)
	if int(m["offset"].(float64)) != 90 || m["has_more"] != false {
		t.Fatalf("offset=%v has_more=%v", m["offset"], m["has_more"])
	}
}

func TestReadSessionRejectsBadModeValues(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 10)
	for _, args := range []map[string]any{
		{"uuid": "P", "around_sequence": 500},
		{"uuid": "P", "around_sequence": -1},
		{"uuid": "P", "around_sequence": "x"},
		{"uuid": "P", "around_sequence": 1.5},
		{"uuid": "P", "last_n": 0},
		{"uuid": "P", "last_n": -3},
		{"uuid": "P", "last_n": "x"},
	} {
		if r := call(t, handleReadSession(d, nil), args); !r.IsError {
			t.Errorf("%v: expected a tool error", args)
		}
	}
}

func TestReadSessionLastNIsCappedByLimit(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 100)
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "last_n": 100, "limit": 10}))
	wantSeqs(t, seqs(t, m), 90, 99)
}

func TestReadSessionPlainOffsetReportsTotal(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 12)
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "offset": 4, "limit": 3}))
	if int(m["total"].(float64)) != 12 {
		t.Fatalf("total=%v want 12", m["total"])
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/linhn0617/clio/internal/db"
)

// resultLen measures what the client actually receives, in the budget's unit
// (UTF-16 code units, so an astral character such as an emoji counts 2):
// every text content plus structuredContent if the handler attached one (a
// second copy of the payload, which the budget must also cover).
func resultLen(t *testing.T, r *mcp.CallToolResult) int {
	t.Helper()
	n := 0
	for _, c := range r.Content {
		tc, ok := c.(mcp.TextContent)
		if !ok {
			t.Fatalf("content not text: %T", c)
		}
		n += len(utf16.Encode([]rune(tc.Text)))
	}
	if r.StructuredContent != nil {
		b, err := json.Marshal(r.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		n += len(utf16.Encode([]rune(string(b))))
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

// bigDataset seeds realistic-size rows (ordinary project paths, ~250-char
// messages, long but plausible shell commands, 800-char transcript messages):
// no single row comes near even the 4,000 floor, so every tool must fit its
// budget by dropping whole rows. The activity and read_session responses
// exceed 48,000; the others exceed only the floor.
func bigDataset(t *testing.T, d *db.DB) {
	t.Helper()
	cmd := "cat <<'EOF' > deploy.sh\n" + strings.Repeat("kubectl rollout status deploy/api --timeout=120s && ", 22) + "\nEOF"
	for i := range 50 {
		id := fmt.Sprintf("s%02d", i)
		addSession(t, d, id, fmt.Sprintf("/home/dev/src/github.com/acme/service-%02d", i))
		addMsg(t, d, id, 0, "user", "needlehaystack authentication failure "+strings.Repeat("filler ", 30))
		addTarget(t, d, id, "command", fmt.Sprintf("%02d %s", i, cmd))
	}
	addSession(t, d, "R", "/r")
	for i := range 200 {
		addMsg(t, d, "R", i, "user", strings.Repeat("m", 800))
	}
}

func TestBudgetFinalResultWithinMaxCharsForEveryTool(t *testing.T) {
	d := testDB(t)
	bigDataset(t, d)
	all := []int{4000, 40000, 48000}
	tools := map[string]struct {
		run          func(max int) *mcp.CallToolResult
		mustTruncate []int // budgets this dataset exceeds; under the others it fits whole
	}{
		"search": {func(max int) *mcp.CallToolResult {
			return call(t, handleSearch(d, nil), map[string]any{"query": "needlehaystack", "limit": 50, "max_chars": max})
		}, []int{4000}},
		"list_sessions": {func(max int) *mcp.CallToolResult {
			return call(t, handleListSessions(d, nil), map[string]any{"limit": 50, "max_chars": max})
		}, []int{4000}},
		"activity_summary": {func(max int) *mcp.CallToolResult {
			return call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "command", "limit": 50, "max_chars": max})
		}, all},
		"ask": {func(max int) *mcp.CallToolResult {
			return call(t, handleAsk(d, nil), map[string]any{"question": "authentication failure", "limit": 10, "max_tokens": 8000, "max_chars": max})
		}, []int{4000}},
		"read_session": {func(max int) *mcp.CallToolResult {
			return call(t, handleReadSession(d, nil), map[string]any{"uuid": "R", "limit": 200, "max_chars": max})
		}, all},
	}
	for name, tc := range tools {
		for _, max := range all {
			r := tc.run(max)
			if r.StructuredContent != nil {
				t.Errorf("%s/%d: structuredContent duplicates the payload", name, max)
			}
			m := resultJSON(t, r)
			if n := resultLen(t, r); n > max {
				t.Errorf("%s/%d: final result is %d chars, over budget", name, max, n)
			}
			if next, _ := m["next"].(string); strings.Contains(next, "fixed fields") {
				t.Errorf("%s/%d: realistic rows must not hit the fixed-fields fallback: %s", name, max, next)
			}
			if want := slices.Contains(tc.mustTruncate, max); (m["truncated"] == true) != want {
				t.Errorf("%s/%d: truncated=%v, want %v", name, max, m["truncated"], want)
			}
		}
	}
}

func TestBudgetCountsCharactersNotBytes(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 20 {
		addMsg(t, d, "P", i, "user", strings.Repeat("字", 600))
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 4000})
	m := resultJSON(t, r)
	if n := resultLen(t, r); n > 4000 {
		t.Fatalf("%d chars over budget", n)
	}
	// 600 CJK characters per message (one UTF-16 unit each): ~5 fit; a byte
	// count would allow only ~1.
	if kept := len(m["messages"].([]any)); kept < 4 {
		t.Fatalf("budget must count characters, not bytes: kept only %d", kept)
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
	// The row cannot be made to fit, so it is dropped rather than sent over budget.
	if n := len(m["sessions"].([]any)); n != 0 || m["count"].(float64) != 0 {
		t.Fatalf("an unfittable row must not be returned: %d rows, count=%v", n, m["count"])
	}
	if n := resultLen(t, r); n > 4000 {
		t.Fatalf("response %d chars over budget", n)
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

// ---- review round 3 ----

// Each session's hit is preceded by two long context messages ("<" escapes
// to six characters), so cutting excerpts in their original order would keep
// a leading context excerpt and nothing else: a group without its hit.
func TestBudgetAskKeepsHitWhenContextPrecedesIt(t *testing.T) {
	d := testDB(t)
	for i := range 3 {
		id := fmt.Sprintf("s%d", i)
		addSession(t, d, id, "/p")
		addMsg(t, d, id, 0, "user", strings.Repeat("<", 600))
		addMsg(t, d, id, 1, "assistant", strings.Repeat("<", 600))
		addMsg(t, d, id, 2, "user", "authentication failure")
	}
	r := call(t, handleAsk(d, nil), map[string]any{"question": "authentication failure", "max_chars": 4000})
	m := wantTruncated(t, r, 4000)
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
			t.Fatalf("every kept group must contain its hit excerpt: %v", g)
		}
	}
}

// The tools whose single row carries uncuttable fields (paths, titles, ids)
// that can on their own exceed the budget. For them the fallback is zero rows
// plus a fixed-fields hint, and the response still fits. Every other tool's
// row is cut by its text instead (TestBudgetCuttableRowsAreCutNotDropped).
func TestBudgetFixedFieldsFallBackToNoRows(t *testing.T) {
	long := "/p/" + strings.Repeat("x", 6000)
	d := testDB(t)
	addSession(t, d, "s1", long)
	addMsg(t, d, "s1", 0, "user", "needlehaystack authentication failure")
	addUsage(t, d, "s1", "claude-code", "model-one", 100, 150)
	for name, tc := range map[string]struct {
		args map[string]any
		h    func(d *db.DB) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
		rows string
	}{
		"search": {map[string]any{"query": "needlehaystack"}, func(d *db.DB) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleSearch(d, nil)
		}, "results"},
		"list_sessions": {map[string]any{}, func(d *db.DB) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleListSessions(d, nil)
		}, "sessions"},
		"activity/project": {map[string]any{"group_by": "project"}, func(d *db.DB) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleActivitySummary(d, nil)
		}, "buckets"},
		"activity/usage": {map[string]any{"group_by": "usage", "source": "all"}, func(d *db.DB) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleActivitySummary(d, nil)
		}, "sessions"},
		"ask": {map[string]any{"question": "authentication failure"}, func(d *db.DB) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleAsk(d, nil)
		}, "groups"},
	} {
		tc.args["max_chars"] = 4000
		r := call(t, tc.h(d), tc.args)
		m := resultJSON(t, r)
		if next, _ := m["next"].(string); m["truncated"] != true || !strings.Contains(next, "fixed fields exceed max_chars=4000") {
			t.Errorf("%s: want truncated + fixed-fields hint, got truncated=%v next=%v", name, m["truncated"], m["next"])
		}
		if rows, _ := m[tc.rows].([]any); len(rows) != 0 {
			t.Errorf("%s: an unfittable row must be dropped, got %d", name, len(rows))
		}
		if n := resultLen(t, r); n > 4000 {
			t.Errorf("%s: response %d chars over budget", name, n)
		}
	}
}

// Rows made of fixed-size fields plus one free-text field never take the
// zero-row path: the text is cut instead.
func TestBudgetCuttableRowsAreCutNotDropped(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addMsg(t, d, "P", 0, "user", strings.Repeat("a", 60000))
	addTarget(t, d, "P", "command", strings.Repeat("c", 60000))
	for name, r := range map[string]*mcp.CallToolResult{
		"read_session":     call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 4000}),
		"activity/command": call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "command", "max_chars": 4000}),
	} {
		m := wantTruncated(t, r, 4000)
		if next := m["next"].(string); strings.Contains(next, "fixed fields") || !strings.Contains(next, "content was cut") {
			t.Errorf("%s: want the content-cut hint, got %q", name, next)
		}
		rows, _ := m["messages"].([]any)
		if rows == nil {
			rows, _ = m["activity"].([]any)
		}
		if len(rows) != 1 {
			t.Errorf("%s: the cut row must be returned, got %d", name, len(rows))
		}
	}
}

// When the whole response fits, the subagent list is never cut, however much
// of the budget its inlined messages take.
func TestBudgetSubagentListKeptWhenResponseFits(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addMsg(t, d, "P", 0, "user", "hi")
	for i := range 5 {
		c := fmt.Sprintf("agent-%d", i)
		addChild(t, d, c, "/p", "P", "general-purpose")
		for j := range 10 {
			addMsg(t, d, c, j, "assistant", strings.Repeat("s", 600))
		}
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "include_subagents": true})
	m := resultJSON(t, r)
	if _, has := m["truncated"]; has {
		t.Fatalf("a response that fits must not be truncated: next=%v", m["next"])
	}
	subs := m["subagents"].([]any)
	if len(subs) != 5 {
		t.Fatalf("all 5 subagents fit, got %d", len(subs))
	}
	for _, s := range subs {
		if n := len(s.(map[string]any)["messages"].([]any)); n != 10 {
			t.Fatalf("subagent messages must be kept: %d", n)
		}
	}
	if n := resultLen(t, r); n <= defaultMaxChars/2 || n > defaultMaxChars {
		t.Fatalf("fixture must fill more than half the budget but fit it: %d", n)
	}
}

// When the response overflows, the half-budget cap on the subagent list
// measures the list entries alone: one subagent whose inlined messages take
// more than half the budget is still listed, and keeps its messages while
// the parent page shrinks around them.
func TestBudgetSubagentListCapIgnoresInlinedMessages(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 60 {
		addMsg(t, d, "P", i, "user", strings.Repeat("m", 800))
	}
	addChild(t, d, "agent-c", "/p", "P", "general-purpose")
	for i := range 25 {
		addMsg(t, d, "agent-c", i, "assistant", strings.Repeat("s", 1000))
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "limit": 200, "include_subagents": true})
	m := wantTruncated(t, r, defaultMaxChars)
	subs := m["subagents"].([]any)
	if len(subs) != 1 {
		t.Fatalf("the one subagent must stay listed, got %d (next=%q)", len(subs), m["next"])
	}
	if msgs, _ := subs[0].(map[string]any)["messages"].([]any); len(msgs) != 25 {
		t.Fatalf("its messages fit beside a shorter parent page and must stay: %d", len(msgs))
	}
	if kept := len(m["messages"].([]any)); kept == 0 || kept >= 60 {
		t.Fatalf("parent page should shrink to a non-empty prefix, got %d", kept)
	}
}

// A large parent page is shrunk before the inlined subagent messages are
// dropped; they go only when even one parent message cannot sit beside them.
func TestBudgetSubagentMessagesKeptByShrinkingParentPage(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 20 {
		addMsg(t, d, "P", i, "user", strings.Repeat("m", 800))
	}
	addChild(t, d, "agent-c", "/p", "P", "general-purpose")
	for i := range 3 {
		addMsg(t, d, "agent-c", i, "assistant", strings.Repeat("s", 300))
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "include_subagents": true, "max_chars": 4000})
	m := wantTruncated(t, r, 4000)
	if kept := len(m["messages"].([]any)); kept == 0 || kept >= 20 {
		t.Fatalf("parent page should shrink to a non-empty prefix, got %d", kept)
	}
	sub := m["subagents"].([]any)[0].(map[string]any)
	if msgs, _ := sub["messages"].([]any); len(msgs) != 3 {
		t.Fatalf("subagent messages must survive while the parent page can shrink: %v", sub)
	}
	if next := m["next"].(string); strings.Contains(next, "subagent messages omitted") {
		t.Fatalf("no subagent messages were omitted: %q", next)
	}
}

func TestBudgetHintSaysContentWasCutWhenRowsAlsoDropped(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addMsg(t, d, "P", 0, "user", strings.Repeat("a", 6000))
	addMsg(t, d, "P", 1, "user", strings.Repeat("b", 6000))
	m := wantTruncated(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 4000}), 4000)
	next := m["next"].(string)
	if !strings.Contains(next, "continue with offset=1") || !strings.Contains(next, "content was cut") {
		t.Fatalf("hint must cover both the dropped message and the cut one: %q", next)
	}
}

func TestBudgetHintKeepsSubagentNoteAlongsideContentCut(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addMsg(t, d, "P", 0, "user", strings.Repeat("a", 6000))
	addChild(t, d, "agent-c", "/p", "P", "general-purpose")
	for i := range 20 {
		addMsg(t, d, "agent-c", i, "assistant", strings.Repeat("s", 800))
	}
	m := wantTruncated(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "include_subagents": true, "max_chars": 4000}), 4000)
	next := m["next"].(string)
	if !strings.Contains(next, "subagent messages omitted") || !strings.Contains(next, "content was cut") {
		t.Fatalf("hint must keep the subagent note and add the content cut: %q", next)
	}
}

func TestBudgetReadSessionHintNamesRequestedCount(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 5 {
		addMsg(t, d, "P", i, "user", strings.Repeat("m", 1500))
	}
	m := wantTruncated(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 4000}), 4000)
	if next := m["next"].(string); !strings.Contains(next, fmt.Sprintf("of the %d requested", defaultReadLimit)) {
		t.Fatalf("hint must name the %d messages requested: %q", defaultReadLimit, next)
	}
}

// Emoji are one code point but two UTF-16 units; a code-point budget would
// let these responses reach about twice max_chars in UTF-16.
func TestBudgetCountsUTF16UnitsForAstralCharacters(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	for i := range 20 {
		addMsg(t, d, "P", i, "user", strings.Repeat("😀", 600))
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 4000})
	m := wantTruncated(t, r, 4000)
	if kept := len(m["messages"].([]any)); kept == 0 || kept > 3 {
		t.Fatalf("600 emoji = 1200 units per message: 1..3 fit in 4000, got %d", kept)
	}
	// A single oversized emoji message is cut on a character boundary.
	d2 := testDB(t)
	addSession(t, d2, "Q", "/p")
	addMsg(t, d2, "Q", 0, "user", strings.Repeat("😀", 5000))
	r = call(t, handleReadSession(d2, nil), map[string]any{"uuid": "Q", "max_chars": 4000})
	m = wantTruncated(t, r, 4000)
	c := m["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.HasPrefix(c, "😀😀") || strings.ContainsRune(c, utf8.RuneError) {
		t.Fatalf("content must keep whole emoji: %q...", c[:16])
	}
	if !strings.Contains(c, "[truncated ") {
		t.Fatalf("missing truncation marker")
	}
}

func TestReadSessionModeValuesAcceptNumericStrings(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 100)
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "last_n": "5"}))
	wantSeqs(t, seqs(t, m), 95, 99)
	m = resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": "50", "limit": "10"}))
	wantSeqs(t, seqs(t, m), 45, 54)
}

// last_n counts the messages and then fetches the tail; a message written in
// between must not shift the page off the tail it counted.
func TestReadSessionLastNReadsOneSnapshot(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 30)
	w, err := db.Open(d.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	afterReadSessionCount = func() { addMsg(t, w, "P", 30, "user", "late") }
	t.Cleanup(func() { afterReadSessionCount = nil })
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "last_n": 5}))
	wantSeqs(t, seqs(t, m), 25, 29)
	if m["has_more"] != false || int(m["total"].(float64)) != 30 {
		t.Fatalf("page must be the tail of one snapshot: has_more=%v total=%v", m["has_more"], m["total"])
	}
}

// Plain offset mode derives total from the page when the page reaches the
// end, and counts otherwise; both must report the real count.
func TestReadSessionPlainOffsetTotalOnEveryPage(t *testing.T) {
	d := testDB(t)
	seedSeq(t, d, 12)
	for _, c := range []struct{ offset, limit int }{{0, 50}, {8, 10}, {4, 3}, {50, 10}, {12, 5}} {
		m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "offset": c.offset, "limit": c.limit}))
		if int(m["total"].(float64)) != 12 {
			t.Errorf("offset=%d limit=%d: total=%v want 12", c.offset, c.limit, m["total"])
		}
	}
}

func TestReadSessionAroundHiddenToolResultSaysSo(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addMsg(t, d, "P", 0, "user", "u0")
	addMsg(t, d, "P", 1, "tool_result", "t1")
	addMsg(t, d, "P", 2, "assistant", "a2")
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": 1}))
	note, _ := m["note"].(string)
	if !strings.Contains(note, "seq 1") || !strings.Contains(note, "tool_result") || !strings.Contains(note, "include_tool_output") {
		t.Fatalf("want a note naming the hidden tool_result, got %v", m["note"])
	}
	if _, has := m["truncated"]; has {
		t.Fatalf("nothing was cut: %v", m)
	}
	m = resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": 1, "include_tool_output": true}))
	if _, has := m["note"]; has {
		t.Fatalf("a visible target needs no note: %v", m["note"])
	}
	m = resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": 2}))
	if _, has := m["note"]; has {
		t.Fatalf("a visible target needs no note: %v", m["note"])
	}
}

// ---- review round 3, verifier follow-up ----

// The shed decision must measure the text actually sent, hints included: with
// a subagent message just small enough that one parent message fits beside it
// only without the truncated/next fields, the parent page must not vanish.
func TestBudgetSubagentShedMeasuresFinalText(t *testing.T) {
	for size := 3200; size <= 3600; size += 10 {
		d := testDB(t)
		addSession(t, d, "P", "/p")
		addMsg(t, d, "P", 0, "user", strings.Repeat("u", 100))
		addMsg(t, d, "P", 1, "user", strings.Repeat("v", 100))
		addChild(t, d, "agent-c", "/p", "P", "general-purpose")
		addMsg(t, d, "agent-c", 0, "assistant", strings.Repeat("s", size))
		r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "include_subagents": true, "max_chars": 4000})
		m := resultJSON(t, r)
		if n := resultLen(t, r); n > 4000 {
			t.Errorf("size %d: response %d chars over budget", size, n)
		}
		if len(m["messages"].([]any)) == 0 {
			t.Errorf("size %d: a parent message fits alone and must be kept (next=%v)", size, m["next"])
		}
		if next, _ := m["next"].(string); strings.Contains(next, "fixed fields") {
			t.Errorf("size %d: false fixed-fields hint: %q", size, next)
		}
	}
}

func TestReadSessionAroundSequenceOnEmptySession(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": 0})
	if !r.IsError {
		t.Fatal("expected a tool error")
	}
	txt := r.Content[0].(mcp.TextContent).Text
	if strings.Contains(txt, "-1") || !strings.Contains(txt, "no messages") {
		t.Fatalf("error should say the session has no messages, got %q", txt)
	}
}

func TestReadSessionAroundSequenceWithOnlyToolMessages(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p")
	addMsg(t, d, "P", 0, "tool_result", "t0")
	addMsg(t, d, "P", 1, "tool_result", "t1")
	m := resultJSON(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "around_sequence": 1}))
	if n := len(m["messages"].([]any)); n != 0 {
		t.Fatalf("no dialogue messages to show, got %d", n)
	}
	note, _ := m["note"].(string)
	if strings.Contains(note, "centred on") || !strings.Contains(note, "no dialogue messages") || !strings.Contains(note, "include_tool_output") {
		t.Fatalf("note must say there is no dialogue message to centre on, got %q", note)
	}
	// A hidden target after the last dialogue message: the window ends there.
	d2 := testDB(t)
	addSession(t, d2, "Q", "/p")
	addMsg(t, d2, "Q", 0, "user", "u0")
	addMsg(t, d2, "Q", 1, "tool_result", "t1")
	m = resultJSON(t, call(t, handleReadSession(d2, nil), map[string]any{"uuid": "Q", "around_sequence": 1}))
	wantSeqs(t, seqs(t, m), 0, 0)
	if note, _ := m["note"].(string); strings.Contains(note, "next dialogue message") || !strings.Contains(note, "no dialogue message follows") {
		t.Fatalf("note must say no dialogue message follows, got %q", note)
	}
}

// Fields outside the rows (the echoed ask question, a session's path and title)
// can alone exceed max_chars. They are cut first, to half the budget, so rows
// still come back in the other half; short fields such as ids stay whole and
// next names what was cut.
func TestBudgetOversizedFixedFieldsAreCut(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p/"+strings.Repeat("x", 6000))
	for i := range 5 {
		addMsg(t, d, "P", i, "user", "authentication failure while logging in")
	}

	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 4000})
	m := wantTruncated(t, r, 4000)
	sess := m["session"].(map[string]any)
	if sess["uuid"] != "P" {
		t.Fatalf("short fixed fields must stay whole, uuid=%v", sess["uuid"])
	}
	if p, _ := sess["project"].(string); !strings.HasPrefix(p, "/p/xxx") || !strings.Contains(p, "[truncated ") {
		t.Fatalf("oversized project should keep a prefix and a truncation marker, got %.40q…", p)
	}
	if msgs, _ := m["messages"].([]any); len(msgs) != 5 {
		t.Fatalf("cutting the path must leave room for the messages, got %d", len(msgs))
	}
	if next := m["next"].(string); !strings.Contains(next, "long fields were cut to fit max_chars=4000: session.project") || strings.Contains(next, "narrow the query") {
		t.Fatalf("next should name the cut field, not advise narrowing: %q", next)
	}

	// ask: a normal path, so only the echoed question is over budget.
	d2 := testDB(t)
	addSession(t, d2, "Q", "/q")
	for i := range 5 {
		addMsg(t, d2, "Q", i, "user", "authentication failure while logging in")
	}
	r2 := call(t, handleAsk(d2, nil), map[string]any{"question": strings.Repeat("authentication failure ", 300), "max_chars": 4000})
	m2 := wantTruncated(t, r2, 4000)
	if q, _ := m2["question"].(string); !strings.HasPrefix(q, "authentication failure") || !strings.Contains(q, "[truncated ") {
		t.Fatalf("oversized question should be cut with a marker, got %.40q…", q)
	}
	if groups, _ := m2["groups"].([]any); len(groups) == 0 {
		t.Fatal("cutting the question must leave room for evidence groups")
	}
}

// Cutting the fields outside the rows keeps every reason the response was cut,
// including the subagent note read_session set itself.
func TestBudgetFieldCutKeepsSubagentNote(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p/"+strings.Repeat("x", 6000))
	addMsg(t, d, "P", 0, "user", "hello")
	for i := range 200 { // more than fit: the shed must run
		addChild(t, d, fmt.Sprintf("agent-%03d", i), "/p", "P", "general-purpose")
	}
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "include_subagents": true, "max_chars": 4000})
	m := wantTruncated(t, r, 4000)
	next := m["next"].(string)
	for _, want := range []string{"subagents to fit max_chars", "long fields were cut to fit max_chars=4000: session.project"} {
		if !strings.Contains(next, want) {
			t.Errorf("next lacks %q: %q", want, next)
		}
	}
}

// The cut size never shrinks as the field cap grows, so the binary search
// over the cap finds the largest one that fits. Short strings whose marker
// would be longer than they are, CJK (3 bytes, 1 unit) and JSON escapes are
// where a naive cut would make a smaller cap produce a larger payload.
func TestCapFieldsSizeMonotoneInCap(t *testing.T) {
	m := map[string]any{"long": strings.Repeat("長", 3000) + strings.Repeat("\x01", 50), "n": []any{1, 2, 3}}
	for i := range 20 {
		m[fmt.Sprintf("k%02d", i)] = "general"
	}
	m["cjk"] = strings.Repeat("中", 30)
	prev := -1
	for c := 0; c <= 3200; c++ {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		got, _, err := capJSON(raw, c, "")
		if err != nil {
			t.Fatal(err)
		}
		if size := utf16Units(got); size < prev {
			t.Fatalf("size shrank from %d to %d when the cap grew to %d", prev, size, c)
		} else {
			prev = size
		}
	}
}

// Positive case: many short fields beside a long one keep their values whole
// while the long one is cut. (A regression to a non-monotone cut is caught by
// TestCapFieldsSizeMonotoneInCap; this fixture fits either way.)
func TestBudgetFieldCutShortFieldsStayWhole(t *testing.T) {
	render := func(k, textCap int) map[string]any {
		m := map[string]any{"long": strings.Repeat("y", 9000), "nums": make([]int, 400)}
		for i := range 20 {
			m[fmt.Sprintf("short%02d", i)] = "seven77"
		}
		return m
	}
	r := fitBudget(0, 4000, false, render, func(int, int) string { return "" }, nil)
	m := wantTruncated(t, r, 4000)
	if m["short00"] != "seven77" || !strings.Contains(m["long"].(string), "[truncated ") {
		t.Fatalf("short fields must stay whole and the long one be cut: short00=%v", m["short00"])
	}
}

// capJSON keeps key order and exact numbers, cuts long strings, and leaves ids,
// notes and hints whole however small the cap.
func TestCapJSONKeepsOrderNumbersAndIDs(t *testing.T) {
	raw := []byte(`{"z_title":"` + strings.Repeat("t", 80) + `","uuid":"` + strings.Repeat("u", 80) + `","n":9223372036854775807,"session_uuid":"` + strings.Repeat("s", 80) + `","note":"` + strings.Repeat("n", 80) + `","a":[{"id":"` + strings.Repeat("i", 80) + `","ok":true,"x":null}]}`)
	out, cut, err := capJSON(raw, 5, "root")
	if err != nil {
		t.Fatal(err)
	}
	prev := -1
	for _, k := range []string{`"z_title"`, `"uuid"`, `"n"`, `"session_uuid"`, `"note"`, `"a"`} {
		i := strings.Index(string(out), k+":")
		if i <= prev {
			t.Fatalf("key order changed at %s: %s", k, out)
		}
		prev = i
	}
	var v map[string]any
	d := json.NewDecoder(strings.NewReader(string(out)))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v["n"].(json.Number).String() != "9223372036854775807" {
		t.Errorf("number changed: %v", v["n"])
	}
	for _, k := range []string{"uuid", "session_uuid", "note"} {
		if s := v[k].(string); len(s) != 80 {
			t.Errorf("%s must stay whole, got %q", k, s)
		}
	}
	if id := v["a"].([]any)[0].(map[string]any)["id"].(string); len(id) != 80 {
		t.Errorf("nested id must stay whole, got %q", id)
	}
	if title := v["z_title"].(string); !strings.Contains(title, "[truncated ") {
		t.Errorf("long title not cut: %q", title)
	}
	if strings.Join(cut, ",") != "root.z_title" {
		t.Errorf("cut paths = %v", cut)
	}
}

// A long path is cut before the subagent shed, so it does not push the
// subagent list out of a response that has room for it once the path is cut.
func TestBudgetFieldCutBeforeSubagentShed(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p/"+strings.Repeat("x", 6000))
	for i := range 50 {
		addMsg(t, d, "P", i, "user", strings.Repeat("m", 200))
	}
	for i := range 3 {
		addChild(t, d, fmt.Sprintf("agent-%d", i), "/p", "P", "general-purpose")
	}
	// Rows overflow too, so the shed runs: it must measure the cut path.
	m := wantTruncated(t, call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "include_subagents": true, "max_chars": 4000}), 4000)
	if subs, _ := m["subagents"].([]any); len(subs) != 3 {
		t.Fatalf("all 3 subagents fit once the path is cut, got %d (next=%v)", len(subs), m["next"])
	}
	if msgs, _ := m["messages"].([]any); len(msgs) == 0 {
		t.Fatal("messages must still be returned")
	}
}

// Ids and notes stay whole at the top level too, whatever their key's case.
func TestCapJSONKeepsTopLevelNotesAndParentIDs(t *testing.T) {
	long := strings.Repeat("n", 200)
	for _, key := range []string{"note", "Note", "parent_session", "next"} {
		raw, _ := json.Marshal(long)
		out, cut, err := capJSON(raw, 10, key)
		if err != nil || string(out) != string(raw) || len(cut) != 0 {
			t.Errorf("%s: top-level value must stay whole, got %.30s… cut=%v err=%v", key, out, cut, err)
		}
	}
	raw := []byte(`{"parent_session":"` + long + `","Note":"` + long + `","title":"` + long + `"}`)
	_, cut, _ := capJSON(raw, 10, "k")
	if strings.Join(cut, ",") != "k.title" {
		t.Errorf("only the title may be cut, got %v", cut)
	}
}

// A field the cut leaves alone keeps its own encoding: re-encoding would turn
// the � escape json.Marshal writes for invalid UTF-8 into a raw U+FFFD.
func TestBudgetUncutFieldsKeepEncoding(t *testing.T) {
	d := testDB(t)
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, ended_at, turn_count, title) VALUES ('P', ?, 'P.jsonl', 1, 1, ?)`,
		"/p/"+strings.Repeat("x", 6000), "t\xffx"); err != nil {
		t.Fatal(err)
	}
	addMsg(t, d, "P", 0, "user", "hello")
	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "P", "max_chars": 4000})
	wantTruncated(t, r, 4000)
	if text := r.Content[0].(mcp.TextContent).Text; !strings.Contains(text, `"title":"t\ufffdx"`) {
		t.Fatalf("uncut title re-encoded: %.200s", text[strings.Index(text, `"title"`):])
	}
}

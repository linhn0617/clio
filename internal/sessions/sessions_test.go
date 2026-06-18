package sessions

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

// A cancelled context aborts a query rather than running it to completion.
func TestListSessionsRespectsContextCancellation(t *testing.T) {
	d := testDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ListSessions(ctx, d, ListFilter{}); err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
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

func addTarget(t *testing.T, d *db.DB, sess, kind, value string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (0,?,?,?,?)`,
		sess, time.Now().Unix(), kind, value); err != nil {
		t.Fatal(err)
	}
}

func TestListSessionsFilterByTouched(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addSession(t, d, "s2", "/p", 1)
	addTarget(t, d, "s1", "file", "/x/auth.ts")
	addTarget(t, d, "s2", "file", "/x/other.go")
	got, err := ListSessions(context.Background(), d, ListFilter{Touched: "/x/auth"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UUID != "s1" {
		t.Fatalf("touched filter: got %+v", got)
	}
}

func TestListSessionsFilterByTool(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addSession(t, d, "s2", "/p", 1)
	addTarget(t, d, "s1", "tool", "Bash")
	addTarget(t, d, "s2", "tool", "Edit")
	got, err := ListSessions(context.Background(), d, ListFilter{Tool: "Bash"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UUID != "s1" {
		t.Fatalf("tool filter: got %+v", got)
	}
}

func TestListSessionsFilterByRan(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addSession(t, d, "s2", "/p", 1)
	addTarget(t, d, "s1", "command", "go test ./...")
	addTarget(t, d, "s2", "command", "ls -la")
	got, err := ListSessions(context.Background(), d, ListFilter{Ran: "go test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UUID != "s1" {
		t.Fatalf("ran filter: got %+v", got)
	}
}

// TargetKind/TargetValue match a tool_targets entry exactly — unlike Touched
// (prefix) and Ran (substring), so an Activity drill matches its grouped value
// and not substring-related neighbours.
func TestListSessionsFilterByExactTarget(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addSession(t, d, "s2", "/p", 1)
	addTarget(t, d, "s1", "command", "go test")
	addTarget(t, d, "s2", "command", "go test ./...")
	got, err := ListSessions(context.Background(), d, ListFilter{TargetKind: "command", TargetValue: "go test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UUID != "s1" {
		t.Fatalf("exact target should match only the exact command, got %+v", got)
	}
}

func TestActivityByKind(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addTarget(t, d, "s1", "file", "/x/a.go")
	addTarget(t, d, "s1", "file", "/x/a.go")
	addTarget(t, d, "s1", "file", "/x/b.go")
	addTarget(t, d, "s1", "tool", "Bash")
	got, err := ActivityByKind(context.Background(), d, "file", 0, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Value != "/x/a.go" || got[0].Count != 2 || got[1].Value != "/x/b.go" || got[1].Count != 1 {
		t.Fatalf("activity by file: got %+v", got)
	}
}

func TestActivityByKindFiltersSinceAndProject(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/proj/a", 1)
	addSession(t, d, "s2", "/proj/b", 1)
	old := time.Now().Add(-30 * 24 * time.Hour).Unix()
	recent := time.Now().Unix()
	if _, err := d.Exec(`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (0,'s1',?,'file','/x/a.go')`, recent); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (0,'s2',?,'file','/x/b.go')`, old); err != nil {
		t.Fatal(err)
	}

	since := time.Now().Add(-7 * 24 * time.Hour).Unix()
	got, err := ActivityByKind(context.Background(), d, "file", since, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "/x/a.go" {
		t.Fatalf("since filter: got %+v", got)
	}

	got2, err := ActivityByKind(context.Background(), d, "file", 0, "/proj/b", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 1 || got2[0].Value != "/x/b.go" {
		t.Fatalf("project filter: got %+v", got2)
	}
}

func TestGetRecall(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/proj", 5)
	addSession(t, d, "other", "/elsewhere", 3)
	addTarget(t, d, "s1", "file", "/proj/a.go")
	addTarget(t, d, "s1", "command", "go test ./...")
	addTarget(t, d, "other", "file", "/elsewhere/x.go")
	addTarget(t, d, "other", "command", "ls")

	r, err := GetRecall(context.Background(), d, "/proj", 0, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sessions) != 1 || r.Sessions[0].UUID != "s1" {
		t.Fatalf("sessions scoped to project: %+v", r.Sessions)
	}
	if len(r.Files) != 1 || r.Files[0].Value != "/proj/a.go" {
		t.Fatalf("files scoped to project: %+v", r.Files)
	}
	if len(r.Commands) != 1 || r.Commands[0].Value != "go test ./..." {
		t.Fatalf("commands scoped to project: %+v", r.Commands)
	}
}

func TestResolvePrefixExact(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abcdef12-3456", "/p", 1)
	s, err := ResolvePrefix(context.Background(), d, "abcdef12-3456")
	if err != nil || s.UUID != "abcdef12-3456" {
		t.Fatalf("exact resolve failed: %v %+v", err, s)
	}
}

func TestResolvePrefixUnambiguous(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abcdef12-3456", "/p", 1)
	addSession(t, d, "ffffffff-0000", "/p", 1)
	s, err := ResolvePrefix(context.Background(), d, "abc")
	if err != nil || s.UUID != "abcdef12-3456" {
		t.Fatalf("prefix resolve failed: %v %+v", err, s)
	}
}

func TestResolvePrefixAmbiguous(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abc111", "/p", 1)
	addSession(t, d, "abc222", "/p", 1)
	if _, err := ResolvePrefix(context.Background(), d, "abc"); err != ErrAmbiguous {
		t.Fatalf("expected ErrAmbiguous, got %v", err)
	}
}

func TestResolvePrefixNotFound(t *testing.T) {
	d := testDB(t)
	if _, err := ResolvePrefix(context.Background(), d, "nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResolvePrefixExactWinsOverPrefixMatches(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abc", "/p", 1)
	addSession(t, d, "abcd", "/p", 1)
	addSession(t, d, "abcde", "/p", 1)

	// "abc" is itself an exact uuid AND a prefix of two others; exact must win.
	s, err := ResolvePrefix(context.Background(), d, "abc")
	if err != nil || s.UUID != "abc" {
		t.Fatalf("exact-over-prefix: want abc, got %+v err=%v", s, err)
	}
	// "abcd" is an exact uuid AND a prefix of "abcde".
	s, err = ResolvePrefix(context.Background(), d, "abcd")
	if err != nil || s.UUID != "abcd" {
		t.Fatalf("exact-over-prefix: want abcd, got %+v err=%v", s, err)
	}
	// "ab" has no exact match but 3 prefix matches → ambiguous.
	if _, err := ResolvePrefix(context.Background(), d, "ab"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("want ErrAmbiguous for 'ab', got %v", err)
	}
	// Unknown prefix.
	if _, err := ResolvePrefix(context.Background(), d, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for 'zzz', got %v", err)
	}
	// Full uuid still resolves.
	s, err = ResolvePrefix(context.Background(), d, "abcde")
	if err != nil || s.UUID != "abcde" {
		t.Fatalf("full-uuid resolve: want abcde, got %+v err=%v", s, err)
	}
	// Empty prefix must not panic; just returns an error.
	if _, err := ResolvePrefix(context.Background(), d, ""); err == nil {
		t.Fatal("empty prefix should return an error")
	}
}

func TestResolvePrefixEscapesUnderscore(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "a_x", "/p", 1)
	addSession(t, d, "abx", "/p", 1)
	// Exact "a_x" must resolve to exactly "a_x".
	s, err := ResolvePrefix(context.Background(), d, "a_x")
	if err != nil || s.UUID != "a_x" {
		t.Fatalf("underscore escape: want a_x, got %+v err=%v", s, err)
	}
	// Underscore must be literal, not a single-char wildcard. "a_" has no exact
	// row; with an unescaped LIKE it would match both "a_x" and "abx" → ambiguous.
	// With proper escaping it matches only "a_x".
	s, err = ResolvePrefix(context.Background(), d, "a_")
	if err != nil || s.UUID != "a_x" {
		t.Fatalf("underscore wildcard leak: want unique a_x, got %+v err=%v", s, err)
	}
}

func TestResolvePrefixEscapesPercent(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "a%x", "/p", 1)
	addSession(t, d, "aYx", "/p", 1)
	// Exact "a%x" must resolve to exactly "a%x".
	s, err := ResolvePrefix(context.Background(), d, "a%x")
	if err != nil || s.UUID != "a%x" {
		t.Fatalf("percent escape: want a%%x, got %+v err=%v", s, err)
	}
	// Percent must be literal. "a%" has no exact row; unescaped it matches both
	// "a%x" and "aYx" → ambiguous. Escaped it matches only "a%x".
	s, err = ResolvePrefix(context.Background(), d, "a%")
	if err != nil || s.UUID != "a%x" {
		t.Fatalf("percent wildcard leak: want unique a%%x, got %+v err=%v", s, err)
	}
}

func TestGetMessagesPaginationAndHasMore(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 5)
	for i := range 5 {
		addMsg(t, d, "s1", i, "user", "m")
	}
	page, hasMore, err := GetMessages(context.Background(), d, "s1", 0, 2, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || !hasMore {
		t.Fatalf("page=%d hasMore=%v want 2,true", len(page), hasMore)
	}
	last, hasMore, _ := GetMessages(context.Background(), d, "s1", 4, 2, false, true)
	if len(last) != 1 || hasMore {
		t.Fatalf("last page=%d hasMore=%v want 1,false", len(last), hasMore)
	}
}

func TestGetMessagesExcludesToolOutput(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addMsg(t, d, "s1", 0, "user", "hi")
	addMsg(t, d, "s1", 1, "tool_result", "tool noise")
	page, _, _ := GetMessages(context.Background(), d, "s1", 0, 50, false, true)
	if len(page) != 1 || page[0].Role != "user" {
		t.Fatalf("expected only user msg, got %+v", page)
	}
	all, _, _ := GetMessages(context.Background(), d, "s1", 0, 50, true, true)
	if len(all) != 2 {
		t.Fatalf("with tool output expected 2, got %d", len(all))
	}
}

// includeRaw=false omits the heavy raw_json column while keeping content intact,
// so the high-frequency TUI preview path doesn't pull it off disk.
func TestGetMessagesOmitsRawWhenNotIncluded(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addMsg(t, d, "s1", 0, "user", "hello")
	full, _, err := GetMessages(context.Background(), d, "s1", 0, 50, true, true)
	if err != nil || len(full) != 1 || full[0].RawJSON == "" {
		t.Fatalf("with includeRaw the row should carry raw_json: %+v err=%v", full, err)
	}
	lite, _, err := GetMessages(context.Background(), d, "s1", 0, 50, true, false)
	if err != nil || len(lite) != 1 || lite[0].RawJSON != "" {
		t.Fatalf("without includeRaw raw_json should be empty: %+v err=%v", lite, err)
	}
	if lite[0].Content != full[0].Content {
		t.Fatalf("content must be unaffected by omitting raw_json: %q vs %q", lite[0].Content, full[0].Content)
	}
}

func TestListSessionsMinTurns(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addSession(t, d, "s2", "/p", 10)
	rows, err := ListSessions(context.Background(), d, ListFilter{MinTurns: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UUID != "s2" {
		t.Fatalf("min-turns filter failed: %+v", rows)
	}
}

func TestListSessionsProjectPrefixEscaping(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/x/a_b", 1)
	addSession(t, d, "s2", "/x/axb", 1)

	rows, err := ListSessions(context.Background(), d, ListFilter{ProjectPrefix: "/x/a_b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rows {
		if s.UUID == "s2" {
			t.Errorf("project /x/axb should NOT match prefix /x/a_b (underscore must be escaped)")
		}
	}
	found := false
	for _, s := range rows {
		if s.UUID == "s1" {
			found = true
		}
	}
	if !found {
		t.Errorf("project /x/a_b should match prefix /x/a_b, got %+v", rows)
	}
}

func TestListSessionsProjectPercentEscaping(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/x/a%b", 1)
	addSession(t, d, "s2", "/x/axb", 1)

	rows, err := ListSessions(context.Background(), d, ListFilter{ProjectPrefix: "/x/a%b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rows {
		if s.UUID == "s2" {
			t.Errorf("project /x/axb should NOT match prefix /x/a%%b (percent must be escaped)")
		}
	}
}

func TestActivitySummaryGrouping(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/a", 1)
	addMsg(t, d, "s1", 0, "user", "x")
	since := time.Now().Add(-24 * time.Hour).Unix()
	if _, err := ActivitySummary(context.Background(), d, since, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivitySummary(context.Background(), d, since, "day"); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivitySummary(context.Background(), d, since, "bogus"); err == nil {
		t.Fatal("expected error for invalid group_by")
	}
}

func TestActivitySummaryLocalDay(t *testing.T) {
	if os.Getenv("CLIO_TZ_CHILD") == "" {
		cmd := exec.Command(os.Args[0], "-test.run", "^TestActivitySummaryLocalDay$", "-test.v")
		cmd.Env = append(os.Environ(), "TZ=Asia/Taipei", "CLIO_TZ_CHILD=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child failed: %v\n%s", err, out)
		}
		return
	}
	d := testDB(t)
	// 2026-05-01 20:00 UTC = 2026-05-02 04:00 Taipei → UTC day 05-01, local day 05-02.
	ts := time.Date(2026, 5, 1, 20, 0, 0, 0, time.UTC).Unix()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title) VALUES (?,?,?,?,?,?,?)`,
		"s1", "/p", "s1.jsonl", ts, ts, 1, "t"); err != nil {
		t.Fatal(err)
	}
	addMsg(t, d, "s1", 0, "user", "x")
	buckets, err := ActivitySummary(context.Background(), d, ts-1, "day")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Unix(ts, 0).Local().Format("2006-01-02") // 2026-05-02
	if len(buckets) != 1 || buckets[0].Key != want {
		t.Fatalf("want single bucket %q, got %+v", want, buckets)
	}
}

func addChildSession(t *testing.T, d *db.DB, uuid, project string, turns int, parent, agentType string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, parent_session, agent_type) VALUES (?,?,?,?,?,?,?,?,?)`,
		uuid, project, uuid+".jsonl", time.Now().Unix(), time.Now().Unix(), turns, "title-"+uuid, parent, agentType); err != nil {
		t.Fatal(err)
	}
}

// ListSessions hides subagent children by default (with orphan promotion when the
// parent is absent), can include them, can list one parent's children, and carries
// the parent link, type, and per-parent subagent count.
func TestListSessionsNestsSubagents(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p", 2)                                    // top-level parent
	addChildSession(t, d, "agent-C", "/p", 1, "P", "general-purpose") // child of P
	addChildSession(t, d, "agent-O", "/p", 1, "PX", "Explore")        // orphan: parent PX absent

	listed := func(f ListFilter) map[string]Session {
		t.Helper()
		got, err := ListSessions(context.Background(), d, f)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]Session{}
		for _, s := range got {
			m[s.UUID] = s
		}
		return m
	}

	def := listed(ListFilter{})
	if _, ok := def["P"]; !ok {
		t.Fatal("default list should include top-level parent P")
	}
	if _, ok := def["agent-C"]; ok {
		t.Fatal("default list must hide subagent child agent-C")
	}
	if _, ok := def["agent-O"]; !ok {
		t.Fatal("default list should promote orphan agent-O (parent PX absent)")
	}
	if def["P"].SubagentCount != 1 {
		t.Fatalf("P.SubagentCount=%d want 1", def["P"].SubagentCount)
	}

	all := listed(ListFilter{IncludeSubagents: true})
	if len(all) != 3 {
		t.Fatalf("IncludeSubagents should list all 3, got %d", len(all))
	}
	if all["agent-C"].ParentSession != "P" || all["agent-C"].AgentType != "general-purpose" {
		t.Fatalf("child should carry parent link and type: %+v", all["agent-C"])
	}

	kids := listed(ListFilter{ParentSession: "P"})
	if len(kids) != 1 {
		t.Fatalf("ParentSession=P should list exactly 1 child, got %d", len(kids))
	}
	if _, ok := kids["agent-C"]; !ok {
		t.Fatal("ParentSession=P should list agent-C")
	}
}

// A subagent child is promoted to top-level when its parent is excluded by the
// listing's own filters (e.g. an old parent under --since), so recent subagent
// activity is never hidden behind a filtered-out parent.
func TestListSessionsPromotesChildWhenParentFilteredOut(t *testing.T) {
	d := testDB(t)
	old := time.Now().Add(-10 * 24 * time.Hour).Unix()
	recent := time.Now().Unix()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title) VALUES ('P','/p','P.jsonl',?,?,1,'parent')`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, parent_session, agent_type) VALUES ('agent-c','/p','agent-c.jsonl',?,?,1,'kid','P','general-purpose')`, recent, recent); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-24 * time.Hour).Unix()
	got, err := ListSessions(context.Background(), d, ListFilter{Since: since})
	if err != nil {
		t.Fatal(err)
	}
	var sawChild, sawParent bool
	for _, s := range got {
		switch s.UUID {
		case "agent-c":
			sawChild = true
		case "P":
			sawParent = true
		}
	}
	if sawParent {
		t.Fatal("the old parent should be excluded by --since")
	}
	if !sawChild {
		t.Fatal("a recent subagent must be promoted when its parent is filtered out of the listing, not hidden")
	}
}

// A recent subagent whose parent falls outside the current page (LIMIT) is promoted
// to the listing, not hidden behind an off-page parent.
func TestListSessionsPromotesRecentChildBeyondParentPage(t *testing.T) {
	d := testDB(t)
	ins := func(uuid string, ended int64, parent any) {
		if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, parent_session) VALUES (?,?,?,?,?,1,?,?)`,
			uuid, "/p", uuid+".jsonl", ended, ended, uuid, parent); err != nil {
			t.Fatal(err)
		}
	}
	// C (a subagent of P) is the single most recent session; P is older and would
	// fall outside a one-row page.
	ins("C", 100, "P")
	ins("T1", 90, nil)
	ins("P", 80, nil)
	got, err := ListSessions(context.Background(), d, ListFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UUID != "C" {
		t.Fatalf("the most recent row is subagent C; it must be promoted, not hidden behind its off-page parent: got %+v", got)
	}
}

// Orphan subagents whose parent is not indexed count as separate sessions (the
// COALESCE must collapse only on a parent that actually exists), so two orphans of
// the same absent parent are not merged into one.
func TestActivitySummaryCountsOrphanSubagentsSeparately(t *testing.T) {
	d := testDB(t)
	addChildSession(t, d, "agent-1", "/p", 1, "PX", "general-purpose") // parent PX absent
	addChildSession(t, d, "agent-2", "/p", 1, "PX", "general-purpose")
	addMsg(t, d, "agent-1", 0, "user", "a")
	addMsg(t, d, "agent-2", 0, "user", "b")
	since := time.Now().Add(-24 * time.Hour).Unix()
	buckets, err := ActivitySummary(context.Background(), d, since, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].Sessions != 2 {
		t.Fatalf("two orphans of an absent parent should count as 2 sessions, got %+v", buckets)
	}
}

// A parent session and its subagents count as one session in the summary, while
// the subagents' messages still count.
func TestActivitySummaryCountsParentAndChildrenAsOne(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "P", "/p", 1)
	addChildSession(t, d, "agent-C", "/p", 1, "P", "general-purpose")
	addMsg(t, d, "P", 0, "user", "parent msg")
	addMsg(t, d, "agent-C", 0, "user", "child msg")

	since := time.Now().Add(-24 * time.Hour).Unix()
	buckets, err := ActivitySummary(context.Background(), d, since, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 {
		t.Fatalf("want 1 project bucket, got %+v", buckets)
	}
	if buckets[0].Sessions != 1 {
		t.Fatalf("parent+child should count as 1 session, got %d", buckets[0].Sessions)
	}
	if buckets[0].Messages != 2 {
		t.Fatalf("both messages should still count, got %d", buckets[0].Messages)
	}
}

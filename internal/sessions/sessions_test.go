package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestListSessionsFilterBySource(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "cc-1", "/p", 1) // source NULL -> treated as claude-code
	if _, err := d.Exec(`INSERT INTO sessions(uuid, source_file, turn_count, ended_at, source) VALUES ('cx-1','cx-1.jsonl',1,?,'codex')`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ids := func(f ListFilter) []string {
		got, err := ListSessions(ctx, d, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range got {
			out = append(out, s.UUID)
		}
		return out
	}
	if got := ids(ListFilter{}); len(got) != 1 || got[0] != "cc-1" {
		t.Fatalf("default source: got %v want [cc-1]", got)
	}
	if got := ids(ListFilter{Source: "codex"}); len(got) != 1 || got[0] != "cx-1" {
		t.Fatalf("source=codex: got %v want [cx-1]", got)
	}
	if got := ids(ListFilter{Source: "all"}); len(got) != 2 {
		t.Fatalf("source=all: got %v want 2", got)
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
	got, err := ActivityByKind(context.Background(), d, "file", 0, "", "", 10)
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
	got, err := ActivityByKind(context.Background(), d, "file", since, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "/x/a.go" {
		t.Fatalf("since filter: got %+v", got)
	}

	got2, err := ActivityByKind(context.Background(), d, "file", 0, "/proj/b", "", 10)
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

	r, err := GetRecall(context.Background(), d, "/proj", "", 0, 5, 5)
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
	s, err := ResolvePrefix(context.Background(), d, "abcdef12-3456", "")
	if err != nil || s.UUID != "abcdef12-3456" {
		t.Fatalf("exact resolve failed: %v %+v", err, s)
	}
}

func TestResolvePrefixUnambiguous(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abcdef12-3456", "/p", 1)
	addSession(t, d, "ffffffff-0000", "/p", 1)
	s, err := ResolvePrefix(context.Background(), d, "abc", "")
	if err != nil || s.UUID != "abcdef12-3456" {
		t.Fatalf("prefix resolve failed: %v %+v", err, s)
	}
}

func TestResolvePrefixAmbiguous(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abc111", "/p", 1)
	addSession(t, d, "abc222", "/p", 1)
	if _, err := ResolvePrefix(context.Background(), d, "abc", ""); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("expected ErrAmbiguous, got %v", err)
	}
}

func addCandidate(t *testing.T, d *db.DB, uuid, parent, source, title string, started int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, parent_session, source) VALUES (?,?,?,?,?,?,?,NULLIF(?,''),?)`,
		uuid, "/p", uuid+".jsonl", started, started, 1, title, parent, source); err != nil {
		t.Fatal(err)
	}
}

func ambiguousErr(t *testing.T, d *db.DB, prefix string) *AmbiguousError {
	t.Helper()
	_, err := ResolvePrefix(context.Background(), d, prefix, "")
	var amb *AmbiguousError
	if !errors.As(err, &amb) || !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("want *AmbiguousError matching ErrAmbiguous for %q, got %T %v", prefix, err, err)
	}
	return amb
}

// Every listed id, retyped, resolves to exactly that session.
func assertIDsResolve(t *testing.T, d *db.DB, amb *AmbiguousError) {
	t.Helper()
	for _, c := range amb.Candidates {
		got, err := ResolvePrefix(context.Background(), d, c.ID, "")
		if err != nil || got.UUID != c.UUID {
			t.Errorf("listed id %q does not resolve to %s: got %q err=%v", c.ID, c.UUID, got.UUID, err)
		}
	}
}

// An ambiguous prefix lists at most maxAmbiguousCandidates sessions, newest
// first, with a more-line, a single-line escape-free title and RFC 3339 times
// in Error(); Render takes the caller's formatting.
func TestResolvePrefixAmbiguousListsCandidates(t *testing.T) {
	d := testDB(t)
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local).Unix()
	for i := 0; i < 12; i++ {
		addCandidate(t, d, fmt.Sprintf("abcdef12-%04d", i), "", "claude-code", fmt.Sprintf("title %d\nsecond line\x1b[31m", i), base+int64(i)*3600)
	}
	amb := ambiguousErr(t, d, "abc")
	if len(amb.Candidates) != maxAmbiguousCandidates || !amb.More {
		t.Fatalf("want %d candidates and More, got %d more=%v", maxAmbiguousCandidates, len(amb.Candidates), amb.More)
	}
	if amb.Candidates[0].UUID != "abcdef12-0011" || amb.Candidates[maxAmbiguousCandidates-1].UUID != "abcdef12-0002" {
		t.Fatalf("candidates not newest first: first %s last %s", amb.Candidates[0].UUID, amb.Candidates[len(amb.Candidates)-1].UUID)
	}
	msg := amb.Error()
	for _, want := range []string{`"abc"`, "abcdef12-0011", "title 11 second line", time.Unix(base+11*3600, 0).Format(time.RFC3339), "more"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "abcdef12-0001") || strings.Contains(msg, "\x1b") {
		t.Errorf("message lists an 11th candidate or keeps a terminal escape:\n%s", msg)
	}
	if got := strings.Count(msg, "\n"); got != maxAmbiguousCandidates+1 {
		t.Errorf("want header + %d candidate lines + more line, got %d newlines:\n%s", maxAmbiguousCandidates, got, msg)
	}
	custom := amb.Render(func(int64) string { return "T" }, func(string) string { return "X" })
	if !strings.Contains(custom, "\n  abcdef12-0011  T  X") {
		t.Errorf("Render ignores the caller's formatting:\n%s", custom)
	}
}

// Shown ids are the shortest prefix (at least 8 characters, or the whole id)
// that resolves to that session alone across the whole index, including
// sessions beyond the listed ones, and never another session's exact id.
func TestResolvePrefixAmbiguousIDsResolve(t *testing.T) {
	d := testDB(t)
	addCandidate(t, d, "abc11111-x", "", "claude-code", "a", 3)
	addCandidate(t, d, "abc22222-y", "", "claude-code", "b", 2)
	addCandidate(t, d, "ab1", "", "claude-code", "short", 1)
	amb := ambiguousErr(t, d, "ab")
	ids := map[string]string{}
	for _, c := range amb.Candidates {
		ids[c.UUID] = c.ID
	}
	if ids["abc11111-x"] != "abc11111" || ids["abc22222-y"] != "abc22222" || ids["ab1"] != "ab1" {
		t.Fatalf("want 8-char ids and the whole short id, got %v", ids)
	}
	assertIDsResolve(t, d, amb)

	// 12 sessions: the 10 newest are listed; an unlisted older one shares the
	// listed m0000010's 9-character prefix, so the shown id must be longer.
	d2 := testDB(t)
	for i := 0; i < 11; i++ {
		addCandidate(t, d2, fmt.Sprintf("m%07d-x", i), "", "claude-code", "t", int64(100+i))
	}
	addCandidate(t, d2, "m0000010-y", "", "claude-code", "t", 1)
	amb2 := ambiguousErr(t, d2, "m")
	if !amb2.More || amb2.Candidates[0].UUID != "m0000010-x" || amb2.Candidates[0].ID != "m0000010-x" {
		t.Fatalf("listed id must distinguish from the unlisted m0000010-y: %+v", amb2.Candidates[0])
	}
	assertIDsResolve(t, d2, amb2)

	// A prefix that is another session's exact id (in another source) would
	// resolve to that session, so it is not offered.
	d3 := testDB(t)
	addCandidate(t, d3, "abcdefgh-1x", "", "claude-code", "t", 2)
	addCandidate(t, d3, "abcdefgh-2", "", "claude-code", "t", 1)
	addCandidate(t, d3, "abcdefgh-1", "", "codex", "t", 1)
	amb3 := ambiguousErr(t, d3, "abc")
	if amb3.Candidates[0].UUID != "abcdefgh-1x" || amb3.Candidates[0].ID != "abcdefgh-1x" {
		t.Fatalf("offered another session's exact id instead of the full one: %+v", amb3.Candidates[0])
	}
	assertIDsResolve(t, d3, amb3)
}

// Top-level sessions are listed before subagents, which are tagged.
func TestResolvePrefixAmbiguousSubagentsLast(t *testing.T) {
	d := testDB(t)
	addCandidate(t, d, "abc-parent", "", "claude-code", "parent", 50)
	for i := 0; i < 10; i++ {
		addCandidate(t, d, fmt.Sprintf("abc-agent-%d", i), "abc-parent", "claude-code", "parent", int64(60+i))
	}
	addCandidate(t, d, "abc-older", "", "claude-code", "wanted", 10)
	amb := ambiguousErr(t, d, "abc")
	if amb.Candidates[0].UUID != "abc-parent" || amb.Candidates[1].UUID != "abc-older" {
		t.Fatalf("top-level sessions must come first: %s, %s", amb.Candidates[0].UUID, amb.Candidates[1].UUID)
	}
	msg := amb.Error()
	if strings.Count(msg, "[subagent] ") != maxAmbiguousCandidates-2 || strings.Contains(msg, "[subagent] wanted") {
		t.Errorf("subagents not tagged exactly:\n%s", msg)
	}
}

// An id that is not plain printable text is shown quoted and escaped, so two
// ids differing only by a control character stay visibly distinct; multi-byte
// ids are cut on rune boundaries.
func TestResolvePrefixAmbiguousIDDisplay(t *testing.T) {
	d := testDB(t)
	addCandidate(t, d, "abcdefg\x1bh", "", "claude-code", "t", 2)
	addCandidate(t, d, "abcdefgh", "", "claude-code", "t", 1)
	addCandidate(t, d, "abc中文中文中文中文-1", "", "claude-code", "t", 3)
	addCandidate(t, d, "abc中文中文中文中文-2", "", "claude-code", "t", 4)
	msg := ambiguousErr(t, d, "abc").Error()
	if !strings.Contains(msg, `"abcdefg\x1b"`) || strings.Contains(msg, "\x1b") {
		t.Errorf("control character not shown escaped:\n%s", msg)
	}
	if !utf8.ValidString(msg) || !strings.Contains(msg, "abc中文中文中文中文-1 ") {
		t.Errorf("multi-byte id cut mid-rune or not lengthened:\n%s", msg)
	}
}

// SQLite's LIKE decodes invalid UTF-8 differently from Go's bytewise view, so
// when any matching id is invalid UTF-8 (or contains U+FFFD) the candidates are
// listed with whole ids, which always resolve by exact match.
func TestResolvePrefixAmbiguousInvalidUTF8ID(t *testing.T) {
	for _, odd := range []string{"abcdefg\xffX1-long", "abcdefg\uFFFDX1-long"} {
		d := testDB(t)
		addCandidate(t, d, odd, "", "claude-code", "t", 3)
		addCandidate(t, d, "abcdefg\xffY2-long", "", "claude-code", "t", 2)
		addCandidate(t, d, "abcdefgZZ3-long", "", "claude-code", "t", 1)
		amb := ambiguousErr(t, d, "abc")
		for _, c := range amb.Candidates {
			if c.ID != c.UUID {
				t.Errorf("want the whole id beside an invalid one, got %q for %q", c.ID, c.UUID)
			}
		}
		assertIDsResolve(t, d, amb)
	}
}

// A session without a start time shows an empty time, as MCP's other tools do.
func TestAmbiguousErrorZeroTimeEmpty(t *testing.T) {
	amb := &AmbiguousError{Prefix: "ab", Candidates: []Candidate{{Session: Session{UUID: "abc11111-x", Title: "t"}, ID: "abc11111"}}}
	if msg := amb.Error(); !strings.HasSuffix(msg, "\n  abc11111    t") {
		t.Fatalf("zero time not rendered empty:\n%q", msg)
	}
}

// LIKE folds ASCII case, so ids differing only in case share a prefix: the
// shown id must go past the case-folded common prefix.
func TestResolvePrefixAmbiguousCaseFoldedIDs(t *testing.T) {
	d := testDB(t)
	addCandidate(t, d, "ABCDEFGH-1x", "", "claude-code", "t", 2)
	addCandidate(t, d, "abcdefgh-2x", "", "claude-code", "t", 1)
	amb := ambiguousErr(t, d, "abc")
	if amb.Candidates[0].ID != "ABCDEFGH-1" || amb.Candidates[1].ID != "abcdefgh-2" {
		t.Fatalf("want ids past the case-folded common prefix, got %q %q", amb.Candidates[0].ID, amb.Candidates[1].ID)
	}
	assertIDsResolve(t, d, amb)
}

// Ids with zero-width or bidi format characters are shown quoted, so they cannot
// render like another candidate's id or reorder the line.
func TestResolvePrefixAmbiguousInvisibleIDQuoted(t *testing.T) {
	d := testDB(t)
	addCandidate(t, d, "abcdefgh\u200b1", "", "claude-code", "t", 3)
	addCandidate(t, d, "abcdefgh\u202e2", "", "claude-code", "t", 2)
	addCandidate(t, d, "abcdefghij", "", "claude-code", "t", 1)
	msg := ambiguousErr(t, d, "abc").Error()
	if !strings.Contains(msg, `"abcdefgh\u200b"`) || !strings.Contains(msg, `"abcdefgh\u202e"`) || strings.ContainsAny(msg, "\u200b\u202e") {
		t.Fatalf("invisible characters not shown escaped:\n%q", msg)
	}
	if !strings.Contains(msg, "\n  abcdefghi ") {
		t.Fatalf("plain id should stay unquoted:\n%s", msg)
	}
}

// A candidate without a start time keeps the title column aligned.
func TestAmbiguousErrorAlignsMissingTime(t *testing.T) {
	ts := time.Date(2026, 10, 1, 9, 30, 0, 0, time.Local).Unix()
	amb := &AmbiguousError{Prefix: "ab", Candidates: []Candidate{
		{Session: Session{UUID: "abc11111-x", StartedAt: ts, Title: "first"}, ID: "abc11111"},
		{Session: Session{UUID: "abc22222-y", Title: "second"}, ID: "abc22222"},
	}}
	lines := strings.Split(amb.Error(), "\n")
	if a, b := strings.Index(lines[1], "first"), strings.Index(lines[2], "second"); a < 0 || a != b {
		t.Fatalf("title columns differ (%d vs %d):\n%s", a, b, amb.Error())
	}
}

// A whitespace-only id is as missing as an empty one.
func TestResolvePrefixBlankID(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abc111", "/p", 1)
	for _, id := range []string{" ", "\t", " \n "} {
		if _, err := ResolvePrefix(context.Background(), d, id, ""); !errors.Is(err, ErrEmptyID) {
			t.Fatalf("want ErrEmptyID for %q, got %v", id, err)
		}
	}
}

// GetSession is exact: a prefix of an existing id is not found.
func TestGetSessionExactOnly(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abc111", "/p", 1)
	addSession(t, d, "abc222", "/p", 1)
	if s, err := GetSession(context.Background(), d, "abc111"); err != nil || s.UUID != "abc111" {
		t.Fatalf("exact lookup: %+v %v", s, err)
	}
	if _, err := GetSession(context.Background(), d, "abc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("prefix must not resolve in GetSession, got %v", err)
	}
}

// An empty id is a missing argument: it lists nothing.
func TestResolvePrefixEmptyID(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abc111", "/p", 1)
	addSession(t, d, "abc222", "/p", 1)
	if _, err := ResolvePrefix(context.Background(), d, "", ""); !errors.Is(err, ErrEmptyID) {
		t.Fatalf("want ErrEmptyID, got %v", err)
	}
}

func TestResolvePrefixNotFound(t *testing.T) {
	d := testDB(t)
	if _, err := ResolvePrefix(context.Background(), d, "nope", ""); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResolvePrefixExactWinsOverPrefixMatches(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "abc", "/p", 1)
	addSession(t, d, "abcd", "/p", 1)
	addSession(t, d, "abcde", "/p", 1)

	// "abc" is itself an exact uuid AND a prefix of two others; exact must win.
	s, err := ResolvePrefix(context.Background(), d, "abc", "")
	if err != nil || s.UUID != "abc" {
		t.Fatalf("exact-over-prefix: want abc, got %+v err=%v", s, err)
	}
	// "abcd" is an exact uuid AND a prefix of "abcde".
	s, err = ResolvePrefix(context.Background(), d, "abcd", "")
	if err != nil || s.UUID != "abcd" {
		t.Fatalf("exact-over-prefix: want abcd, got %+v err=%v", s, err)
	}
	// "ab" has no exact match but 3 prefix matches → ambiguous.
	if _, err := ResolvePrefix(context.Background(), d, "ab", ""); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("want ErrAmbiguous for 'ab', got %v", err)
	}
	// Unknown prefix.
	if _, err := ResolvePrefix(context.Background(), d, "zzz", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for 'zzz', got %v", err)
	}
	// Full uuid still resolves.
	s, err = ResolvePrefix(context.Background(), d, "abcde", "")
	if err != nil || s.UUID != "abcde" {
		t.Fatalf("full-uuid resolve: want abcde, got %+v err=%v", s, err)
	}
	// Empty prefix must not panic; just returns an error.
	if _, err := ResolvePrefix(context.Background(), d, "", ""); err == nil {
		t.Fatal("empty prefix should return an error")
	}
}

func TestResolvePrefixEscapesUnderscore(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "a_x", "/p", 1)
	addSession(t, d, "abx", "/p", 1)
	// Exact "a_x" must resolve to exactly "a_x".
	s, err := ResolvePrefix(context.Background(), d, "a_x", "")
	if err != nil || s.UUID != "a_x" {
		t.Fatalf("underscore escape: want a_x, got %+v err=%v", s, err)
	}
	// Underscore must be literal, not a single-char wildcard. "a_" has no exact
	// row; with an unescaped LIKE it would match both "a_x" and "abx" → ambiguous.
	// With proper escaping it matches only "a_x".
	s, err = ResolvePrefix(context.Background(), d, "a_", "")
	if err != nil || s.UUID != "a_x" {
		t.Fatalf("underscore wildcard leak: want unique a_x, got %+v err=%v", s, err)
	}
}

func TestResolvePrefixEscapesPercent(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "a%x", "/p", 1)
	addSession(t, d, "aYx", "/p", 1)
	// Exact "a%x" must resolve to exactly "a%x".
	s, err := ResolvePrefix(context.Background(), d, "a%x", "")
	if err != nil || s.UUID != "a%x" {
		t.Fatalf("percent escape: want a%%x, got %+v err=%v", s, err)
	}
	// Percent must be literal. "a%" has no exact row; unescaped it matches both
	// "a%x" and "aYx" → ambiguous. Escaped it matches only "a%x".
	s, err = ResolvePrefix(context.Background(), d, "a%", "")
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

func TestCountMessagesMatchesGetMessagesFilter(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addMsg(t, d, "s1", 0, "user", "u")
	addMsg(t, d, "s1", 1, "tool_result", "t")
	addMsg(t, d, "s1", 2, "assistant", "a")
	ctx := context.Background()
	seq := 2
	for _, c := range []struct {
		tool   bool
		before *int
		want   int
	}{{false, nil, 2}, {true, nil, 3}, {false, &seq, 1}, {true, &seq, 2}} {
		got, err := CountMessages(ctx, d, "s1", c.tool, c.before)
		if err != nil || got != c.want {
			t.Fatalf("tool=%v before=%v: got %d err=%v want %d", c.tool, c.before, got, err, c.want)
		}
	}
}

func TestMessageRole(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 1)
	addMsg(t, d, "s1", 0, "user", "u")
	addMsg(t, d, "s1", 1, "tool_result", "t")
	ctx := context.Background()
	for seq, want := range map[int]string{0: "user", 1: "tool_result", 7: ""} {
		got, err := MessageRole(ctx, d, "s1", seq)
		if err != nil || got != want {
			t.Fatalf("seq %d: got %q err=%v want %q", seq, got, err, want)
		}
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
	if _, err := ActivitySummary(context.Background(), d, since, "project", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivitySummary(context.Background(), d, since, "day", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivitySummary(context.Background(), d, since, "bogus", "", ""); err == nil {
		t.Fatal("expected error for invalid group_by")
	}
}

// ActivitySummary must filter by project prefix under both "project" and "day"
// grouping, matching ActivityByKind's semantics — a project filter must not be
// silently ignored (the tool declares a `project` param, so silently returning
// all-project data is a correctness bug, not just a missing feature).
func TestActivitySummaryFiltersByProject(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/a", 1)
	addSession(t, d, "s2", "/p/b", 1)
	addMsg(t, d, "s1", 0, "user", "x")
	addMsg(t, d, "s2", 0, "user", "y")
	since := time.Now().Add(-24 * time.Hour).Unix()

	// group_by=project: only the matching project's bucket should appear.
	buckets, err := ActivitySummary(context.Background(), d, since, "project", "/p/a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].Key != "/p/a" {
		t.Fatalf("project filter should scope to /p/a only, got %+v", buckets)
	}

	// group_by=day: the bucket's session count must exclude the other project.
	dayBuckets, err := ActivitySummary(context.Background(), d, since, "day", "/p/a", "")
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, b := range dayBuckets {
		total += b.Sessions
	}
	if total != 1 {
		t.Fatalf("day grouping with project filter should count 1 session, got %d across %+v", total, dayBuckets)
	}

	// Empty project prefix must not filter (unchanged default behavior).
	all, err := ActivitySummary(context.Background(), d, since, "project", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("empty project filter should not filter, got %+v", all)
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
	buckets, err := ActivitySummary(context.Background(), d, ts-1, "day", "", "")
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

// A subagent whose parent passes the filters stays hidden even when the parent
// ranks below LIMIT: hiding must not depend on which page the parent lands on.
func TestListSessionsHidesChildWhenParentBeyondPage(t *testing.T) {
	d := testDB(t)
	ins := func(uuid string, ended int64, parent any) {
		if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, parent_session) VALUES (?,?,?,?,?,1,?,?)`,
			uuid, "/p", uuid+".jsonl", ended, ended, uuid, parent); err != nil {
			t.Fatal(err)
		}
	}
	// C (a subagent of P) is the single most recent session; P is older and falls
	// outside a one-row page, but is in the index and passes the (empty) filters.
	ins("C", 100, "P")
	ins("T1", 90, nil)
	ins("P", 80, nil)
	got, err := ListSessions(context.Background(), d, ListFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UUID != "T1" {
		t.Fatalf("subagent C must stay hidden (parent P is indexed); the page should be T1: got %+v", got)
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
	buckets, err := ActivitySummary(context.Background(), d, since, "project", "", "")
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
	buckets, err := ActivitySummary(context.Background(), d, since, "project", "", "")
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

// addSessionAt inserts a top-level session with an explicit ended_at so tests
// can pin recency order instead of racing time.Now().
func addSessionAt(t *testing.T, d *db.DB, uuid, project string, endedAt int64, parent string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, parent_session) VALUES (?,?,?,?,?,?,?,?)`,
		uuid, project, uuid+".jsonl", endedAt-60, endedAt, 2, "title-"+uuid, nullIfEmpty(parent)); err != nil {
		t.Fatal(err)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// The recall digest opens with the last assistant *text* message of the newest
// session as ListSessions orders it; tool_use rows are not text.
func TestGetRecallLastAssistantText(t *testing.T) {
	d := testDB(t)
	addSessionAt(t, d, "old", "/proj", 1000, "")
	addSessionAt(t, d, "new", "/proj", 2000, "")
	addMsg(t, d, "old", 0, "assistant", "old closing words")
	addMsg(t, d, "new", 0, "user", "do the thing")
	addMsg(t, d, "new", 1, "assistant", "Done: the thing. Next: the other thing.")
	addMsg(t, d, "new", 2, "tool_use", "Bash go test ./...")

	r, err := GetRecall(context.Background(), d, "/proj", "", 0, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if r.LastAssistant == nil {
		t.Fatal("expected a last-assistant excerpt")
	}
	if r.LastAssistant.SessionUUID != "new" || r.LastAssistant.Text != "Done: the thing. Next: the other thing." {
		t.Fatalf("got %+v", *r.LastAssistant)
	}
	if r.LastAssistant.TS == 0 {
		t.Fatal("excerpt should carry the message timestamp")
	}
}

// The excerpt ignores the activity window: a session older than --since still
// supplies "where we left off" even though Recent sessions is empty.
func TestGetRecallExcerptIgnoresSince(t *testing.T) {
	d := testDB(t)
	fourWeeksAgo := time.Now().Add(-28 * 24 * time.Hour).Unix()
	addSessionAt(t, d, "old", "/proj", fourWeeksAgo, "")
	if _, err := d.Exec(`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES ('old',0,?, 'assistant','Finished migration','{}')`, fourWeeksAgo); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-14 * 24 * time.Hour).Unix()
	r, err := GetRecall(context.Background(), d, "/proj", "", since, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sessions) != 0 {
		t.Fatalf("Recent sessions stays since-bounded: %+v", r.Sessions)
	}
	if r.LastAssistant == nil || r.LastAssistant.Text != "Finished migration" {
		t.Fatalf("excerpt must survive the since window: %+v", r.LastAssistant)
	}

	// The fallback keeps the digest's page size: a child newer than its (present,
	// also-idle) parent must still be nested, not promoted by a Limit-1 page.
	addSessionAt(t, d, "idle-child", "/proj", fourWeeksAgo+10, "old")
	if _, err := d.Exec(`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES ('idle-child',0,?, 'assistant','child words','{}')`, fourWeeksAgo+10); err != nil {
		t.Fatal(err)
	}
	r, err = GetRecall(context.Background(), d, "/proj", "", since, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if r.LastAssistant == nil || r.LastAssistant.SessionUUID != "old" {
		t.Fatalf("fallback page must nest the child under its parent: %+v", r.LastAssistant)
	}
}

// Inside the window the excerpt is the digest's own first row, even when the
// since-bounded and unbounded listings would nest subagents differently
// (codex review: child C newer than top-level T, parent P outside the window).
func TestGetRecallExcerptMatchesDigestFirstRow(t *testing.T) {
	d := testDB(t)
	addSessionAt(t, d, "P", "/proj", 50, "")
	addSessionAt(t, d, "T", "/proj", 200, "")
	addSessionAt(t, d, "C", "/proj", 300, "P")
	addMsg(t, d, "P", 0, "assistant", "p words")
	addMsg(t, d, "T", 0, "assistant", "t words")
	addMsg(t, d, "C", 0, "assistant", "c words")
	r, err := GetRecall(context.Background(), d, "/proj", "", 100, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sessions) == 0 || r.Sessions[0].UUID != "C" {
		t.Fatalf("since-bounded listing should promote C first: %+v", r.Sessions)
	}
	if r.LastAssistant == nil || r.LastAssistant.SessionUUID != "C" {
		t.Fatalf("excerpt must come from the digest's first row: %+v", r.LastAssistant)
	}
}

// A trailing blank assistant row must not hide the real closing text.
func TestGetRecallSkipsBlankAssistantRow(t *testing.T) {
	d := testDB(t)
	addSessionAt(t, d, "s", "/proj", 1000, "")
	addMsg(t, d, "s", 0, "assistant", "Done")
	addMsg(t, d, "s", 1, "assistant", "   ")
	r, err := GetRecall(context.Background(), d, "/proj", "", 0, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if r.LastAssistant == nil || r.LastAssistant.Text != "Done" {
		t.Fatalf("blank row should be skipped: %+v", r.LastAssistant)
	}
}

// An undated message row falls back to the session's end time; equal ended_at
// resolves by uuid DESC, matching ListSessions.
func TestGetRecallExcerptTSFallbackAndTiebreak(t *testing.T) {
	d := testDB(t)
	addSessionAt(t, d, "aaa", "/proj", 2000, "")
	addSessionAt(t, d, "zzz", "/proj", 2000, "")
	if _, err := d.Exec(`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES ('zzz',0,NULL,'assistant','Done','{}')`); err != nil {
		t.Fatal(err)
	}
	addMsg(t, d, "aaa", 0, "assistant", "not me")
	r, err := GetRecall(context.Background(), d, "/proj", "", 0, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if r.LastAssistant == nil || r.LastAssistant.SessionUUID != "zzz" {
		t.Fatalf("uuid DESC tiebreak should pick zzz: %+v", r.LastAssistant)
	}
	if r.LastAssistant.TS != 2000 {
		t.Fatalf("NULL ts should fall back to ended_at=2000, got %d", r.LastAssistant.TS)
	}
}

func TestGetRecallNoAssistantTextIsNil(t *testing.T) {
	d := testDB(t)
	addSessionAt(t, d, "s", "/proj", 1000, "")
	addMsg(t, d, "s", 0, "user", "hello")
	addMsg(t, d, "s", 1, "tool_use", "Bash ls")
	r, err := GetRecall(context.Background(), d, "/proj", "", 0, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if r.LastAssistant != nil {
		t.Fatalf("no assistant text → nil, got %+v", *r.LastAssistant)
	}
	if len(r.Sessions) != 1 {
		t.Fatalf("rest of the digest must still be built: %+v", r.Sessions)
	}
}

// A subagent child newer than its present parent is hidden by ListSessions, so
// the excerpt comes from the parent; an orphan child (parent absent) is promoted
// and supplies the excerpt, matching what `clio list` shows first.
func TestGetRecallNewestFollowsListSessions(t *testing.T) {
	d := testDB(t)
	addSessionAt(t, d, "parent", "/proj", 1000, "")
	addSessionAt(t, d, "child", "/proj", 2000, "parent")
	addMsg(t, d, "parent", 0, "assistant", "parent words")
	addMsg(t, d, "child", 0, "assistant", "child words")
	r, err := GetRecall(context.Background(), d, "/proj", "", 0, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if r.LastAssistant == nil || r.LastAssistant.SessionUUID != "parent" {
		t.Fatalf("child of a present parent must not win: %+v", r.LastAssistant)
	}

	d2 := testDB(t)
	addSessionAt(t, d2, "orphan", "/proj", 2000, "gone-parent")
	addMsg(t, d2, "orphan", 0, "assistant", "orphan words")
	r2, err := GetRecall(context.Background(), d2, "/proj", "", 0, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if r2.LastAssistant == nil || r2.LastAssistant.SessionUUID != "orphan" {
		t.Fatalf("promoted orphan should supply the excerpt: %+v", r2.LastAssistant)
	}
}

// A project filter matches the path itself and anything beneath it, never a
// sibling that merely shares the prefix (/x/foo must not pull in /x/foo-bar).
func TestListSessionsProjectPathBoundary(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "root", "/x/foo", 1)
	addSession(t, d, "sub", "/x/foo/sub", 1)
	addSession(t, d, "sibling", "/x/foo-bar", 1)
	for _, p := range []string{"/x/foo", "/x/foo/"} {
		rows, err := ListSessions(context.Background(), d, ListFilter{ProjectPrefix: p})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, s := range rows {
			got[s.UUID] = true
		}
		if !got["root"] || !got["sub"] || got["sibling"] || len(rows) != 2 {
			t.Errorf("project %q: want root+sub only, got %+v", p, rows)
		}
	}
}

func TestActivityByKindProjectPathBoundary(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "in", "/x/foo", 1)
	addSession(t, d, "sibling", "/x/foo-bar", 1)
	addTarget(t, d, "in", "file", "/x/foo/a.go")
	addTarget(t, d, "sibling", "file", "/x/foo-bar/b.go")
	got, err := ActivityByKind(context.Background(), d, "file", 0, "/x/foo", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "/x/foo/a.go" {
		t.Fatalf("sibling project leaked into activity: %+v", got)
	}
}

func TestActivitySummaryProjectPathBoundary(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "in", "/x/foo", 1)
	addSession(t, d, "sibling", "/x/foo-bar", 1)
	since := time.Now().Add(-time.Hour).Unix()
	buckets, err := ActivitySummary(context.Background(), d, since, "project", "/x/foo", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].Key != "/x/foo" {
		t.Fatalf("sibling project leaked into summary: %+v", buckets)
	}
}

// The SessionStart hook fires for the session being opened (resume/compact
// reopen an already-indexed one): it and its subagents must not be recalled as
// "the last session", nor feed the files/commands sections.
func TestGetRecallExcludesCurrentSession(t *testing.T) {
	d := testDB(t)
	addSessionAt(t, d, "prev", "/proj", 1000, "")
	addSessionAt(t, d, "cur", "/proj", 3000, "")
	addSessionAt(t, d, "cur-child", "/proj", 3500, "cur")
	addMsg(t, d, "prev", 0, "assistant", "prev closing words")
	addMsg(t, d, "cur", 0, "assistant", "current closing words")
	addMsg(t, d, "cur-child", 0, "assistant", "child closing words")
	addTarget(t, d, "prev", "file", "/proj/prev.go")
	addTarget(t, d, "cur", "file", "/proj/cur.go")
	addTarget(t, d, "cur-child", "file", "/proj/child.go")
	addTarget(t, d, "prev", "command", "make prev")
	addTarget(t, d, "cur", "command", "make cur")
	addTarget(t, d, "cur-child", "command", "make child")

	r, err := GetRecall(context.Background(), d, "/proj", "cur", 0, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sessions) != 1 || r.Sessions[0].UUID != "prev" {
		t.Fatalf("current session (or its subagent) listed: %+v", r.Sessions)
	}
	if r.LastAssistant == nil || r.LastAssistant.SessionUUID != "prev" {
		t.Fatalf("excerpt must come from the previous session: %+v", r.LastAssistant)
	}
	if len(r.Files) != 1 || r.Files[0].Value != "/proj/prev.go" {
		t.Fatalf("files must exclude the current session tree: %+v", r.Files)
	}
	if len(r.Commands) != 1 || r.Commands[0].Value != "make prev" {
		t.Fatalf("commands must exclude the current session tree: %+v", r.Commands)
	}

	// The since-empty fallback honours the exclusion too.
	r, err = GetRecall(context.Background(), d, "/proj", "cur", 2000, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if r.LastAssistant == nil || r.LastAssistant.SessionUUID != "prev" {
		t.Fatalf("fallback excerpt must skip the current session: %+v", r.LastAssistant)
	}
}

// Touched files are limited to paths under the project root, and the limit is
// applied after that filter so out-of-project noise cannot crowd them out.
func TestGetRecallFilesUnderProjectOnly(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/proj", 1)
	for i := 0; i < 5; i++ {
		addTarget(t, d, "s1", "file", "/tmp/scratch/notes.md")
		addTarget(t, d, "s1", "file", "/proj-other/y.go")
	}
	addTarget(t, d, "s1", "file", "/proj/a.go")
	addTarget(t, d, "s1", "file", "/proj/b.go")
	addTarget(t, d, "s1", "file", "/proj/b.go")
	addTarget(t, d, "s1", "command", "ls /tmp")

	r, err := GetRecall(context.Background(), d, "/proj", "", 0, 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 2 || r.Files[0].Value != "/proj/b.go" || r.Files[1].Value != "/proj/a.go" {
		t.Fatalf("want the 2 in-project files, got %+v", r.Files)
	}
	if len(r.Commands) != 1 || r.Commands[0].Value != "ls /tmp" {
		t.Fatalf("commands are not path-filtered: %+v", r.Commands)
	}
}

// codex review #5: SQLite LIKE folds ASCII case, so on a case-sensitive
// filesystem /x/foo must not pull in /x/FOO/sub. LIKE wildcards in the path stay
// literal, and a non-ASCII path compares by the same character count on both sides.
func TestListSessionsProjectPathCaseSensitive(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "root", "/x/foo", 1)
	addSession(t, d, "sub", "/x/foo/sub", 1)
	addSession(t, d, "upper", "/x/FOO/sub", 1)
	addSession(t, d, "us", "/x/a_b/sub", 1)
	addSession(t, d, "usx", "/x/axb/sub", 1)
	addSession(t, d, "pct", "/x/a%b/sub", 1)
	addSession(t, d, "cjk", "/x/中文/sub", 1)
	addSession(t, d, "rel", "rel/path", 1)
	for p, want := range map[string][]string{
		"/x/foo": {"root", "sub"},
		"/x/FOO": {"upper"},
		"/x/a_b": {"us"},
		"/x/a%b": {"pct"},
		"/x/中文":  {"cjk"},
		"/x/中文/": {"cjk"},
		"/":      {"root", "sub", "upper", "us", "usx", "pct", "cjk"},
	} {
		rows, err := ListSessions(context.Background(), d, ListFilter{ProjectPrefix: p})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, s := range rows {
			got[s.UUID] = true
		}
		ok := len(rows) == len(want)
		for _, w := range want {
			ok = ok && got[w]
		}
		if !ok {
			t.Errorf("project %q: want %v, got %v", p, want, got)
		}
	}
}

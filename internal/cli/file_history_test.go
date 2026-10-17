package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/sessions"
)

func TestFormatFileHistoryRows(t *testing.T) {
	rows := []sessions.FileTouch{
		{SessionUUID: "aaaaaaaa-1111", Title: "Fix ranking", TS: 1781333200, Tools: []string{"Edit", "Read"}},
		{SessionUUID: "bbbbbbbb-2222", Title: "Explore", TS: 0, Tools: []string{"Read"}},
	}
	got := formatFileHistory("/p/a.go", rows)
	for _, want := range []string{"/p/a.go", formatTS(1781333200), "Fix ranking", "aaaaaaaa", "Edit,Read", "(undated)", "bbbbbbbb", "read_session"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q\n---\n%s", want, got)
		}
	}
	if strings.Index(got, "aaaaaaaa") > strings.Index(got, "bbbbbbbb") {
		t.Errorf("rows must keep the newest-first order given\n%s", got)
	}
}

func TestFormatFileHistoryEmptyIsSilent(t *testing.T) {
	if got := formatFileHistory("/p/a.go", nil); got != "" {
		t.Errorf("no rows must print nothing, got %q", got)
	}
}

// Every error path prints nothing: missing index, unreadable/invalid DB file.
func TestFileHistoryDigestErrorsAreSilent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "db.sqlite")
	if got := fileHistoryDigest(missing, "/p/a.go", "", 0, 10); got != "" {
		t.Errorf("missing index → %q", got)
	}
	bogus := filepath.Join(t.TempDir(), "db.sqlite")
	if err := os.WriteFile(bogus, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fileHistoryDigest(bogus, "/p/a.go", "", 0, 10); got != "" {
		t.Errorf("invalid DB → %q", got)
	}
}

// A relative spelling resolves to the same key as its absolute form.
func TestFileHistoryKeyRelativeEqualsAbsolute(t *testing.T) {
	orig, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)
	// Resolve through Getwd (not t.TempDir) so macOS's /var → /private/var
	// symlink does not make the two spellings differ for reasons unrelated
	// to the code under test.
	cwd, _ := os.Getwd()
	abs := filepath.Join(cwd, "sub", "a.go")
	if fileHistoryKey("sub/a.go") != fileHistoryKey(abs) {
		t.Errorf("relative %q != absolute %q", fileHistoryKey("sub/a.go"), fileHistoryKey(abs))
	}
}

func TestHelpListsFileHistory(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "file-history") {
		t.Errorf("--help should list file-history\n%s", out.String())
	}
}

// seedFileHistoryDB builds a tiny index with one Claude Code session that
// touched path at ts, returning the DB path.
func seedFileHistoryDB(t *testing.T, path string, ts int64) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "db.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	stmts := []string{
		`INSERT INTO sessions(uuid, project_path, source_file, ended_at, turn_count, title) VALUES ('past-1111','/p','p.jsonl',` + itoa(ts) + `,2,'Fix ranking')`,
		`INSERT INTO messages(id, session_uuid, seq, ts, role, content, raw_json) VALUES (1,'past-1111',0,` + itoa(ts) + `,'tool_use','Edit x','{}')`,
		`INSERT INTO tool_calls(message_id, tool_name, params_summary) VALUES (1,'Edit','x')`,
		`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (1,'past-1111',` + itoa(ts) + `,'file','` + path + `')`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return dbPath
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func readPayload(path string) []byte {
	return []byte(`{"tool_name":"Read","tool_input":{"file_path":"` + path + `"},"session_id":"current-9999","cwd":"/p"}`)
}

func TestFileHistoryHookInjectsFreshHistory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	touch := time.Now().Add(-1 * time.Hour).Unix() // indexed after the file's mtime
	dbPath := seedFileHistoryDB(t, file, touch)

	out := fileHistoryHook(readPayload(file), dbPath, 10)
	var env struct {
		HookSpecificOutput struct {
			HookEventName      string `json:"hookEventName"`
			AdditionalContext  string `json:"additionalContext"`
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("hook output is not the JSON envelope: %q (%v)", out, err)
	}
	h := env.HookSpecificOutput
	if h.HookEventName != "PreToolUse" || h.PermissionDecision != "" {
		t.Errorf("envelope fields wrong: %+v", h)
	}
	for _, want := range []string{"Fix ranking", "past-111", "Edit", "read_session"} {
		if !strings.Contains(h.AdditionalContext, want) {
			t.Errorf("additionalContext missing %q:\n%s", want, h.AdditionalContext)
		}
	}
}

func TestFileHistoryHookSkipsWhenFileNewerThanHistory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	touch := time.Now().Add(-1 * time.Hour).Unix() // file (now) is newer than the touch
	dbPath := seedFileHistoryDB(t, file, touch)
	if out := fileHistoryHook(readPayload(file), dbPath, 10); out != "" {
		t.Errorf("stale history must not be injected, got %q", out)
	}
}

func TestFileHistoryHookMissingFileStillInjects(t *testing.T) {
	file := filepath.Join(t.TempDir(), "gone.go") // never created
	dbPath := seedFileHistoryDB(t, file, time.Now().Unix())
	if out := fileHistoryHook(readPayload(file), dbPath, 10); !strings.Contains(out, "Fix ranking") {
		t.Errorf("stat failure skips the gate, digest expected, got %q", out)
	}
}

func TestFileHistoryHookIgnoresOtherToolsAndBadPayloads(t *testing.T) {
	file := filepath.Join(t.TempDir(), "gone.go")
	dbPath := seedFileHistoryDB(t, file, time.Now().Unix())
	cases := map[string][]byte{
		"Edit tool":         []byte(`{"tool_name":"Edit","tool_input":{"file_path":"` + file + `"},"session_id":"s"}`),
		"empty stdin":       nil,
		"not json":          []byte("nope"),
		"missing file_path": []byte(`{"tool_name":"Read","tool_input":{},"session_id":"s"}`),
	}
	for name, payload := range cases {
		if out := fileHistoryHook(payload, dbPath, 10); out != "" {
			t.Errorf("%s: expected silence, got %q", name, out)
		}
	}
	// The caller's own session is excluded from the rows.
	self := []byte(`{"tool_name":"Read","tool_input":{"file_path":"` + file + `"},"session_id":"past-1111"}`)
	if out := fileHistoryHook(self, dbPath, 10); out != "" {
		t.Errorf("caller's own touch must be excluded → silence, got %q", out)
	}
}

// When not even one row fits the cap, the hook stays silent instead of sending
// an envelope with an empty additionalContext.
func TestFileHistoryHookSilentWhenNothingFits(t *testing.T) {
	long := "/" + strings.Repeat("x", 9000) + "/gone.go" // never created → gate skipped
	dbPath := seedFileHistoryDB(t, fileHistoryKey(long), time.Now().Unix())
	if out := fileHistoryHook(readPayload(long), dbPath, 10); out != "" {
		t.Errorf("expected silence, got %d bytes: %.80s", len(out), out)
	}
}

// The cap holds on the encoded envelope: control characters in the path
// expand to \uXXXX when JSON-encoded.
func TestFileHistoryHookCapHoldsAfterEncoding(t *testing.T) {
	weird := "/" + strings.Repeat("\x01", 1700) + "/gone.go" // never created → gate skipped
	dbPath := seedFileHistoryDB(t, fileHistoryKey(weird), time.Now().Unix())
	out := fileHistoryHook(readPayload(weird), dbPath, 10)
	if n := len([]rune(out)); n > hookMaxChars {
		t.Fatalf("encoded hook output is %d chars > %d", n, hookMaxChars)
	}
}

func TestFormatFileHistoryCapped(t *testing.T) {
	var rows []sessions.FileTouch
	for i := 0; i < 200; i++ {
		rows = append(rows, sessions.FileTouch{SessionUUID: itoa(int64(10000000 + i)), Title: strings.Repeat("t", 60), TS: int64(1781333200 - i), Tools: []string{"Edit", "Read"}})
	}
	got := formatFileHistoryCapped("/p/a.go", rows, hookMaxChars)
	if n := len([]rune(got)); n > hookMaxChars {
		t.Fatalf("capped output is %d chars > %d", n, hookMaxChars)
	}
	if !strings.Contains(got, "more)") {
		t.Errorf("dropped rows must be announced with a (+N more) line:\n%s", got[len(got)-200:])
	}
	// Nothing fits → "" so the hook stays silent instead of sending an empty context.
	if got := formatFileHistoryCapped(strings.Repeat("x", 7990), rows[:1], hookMaxChars); got != "" {
		t.Errorf("expected empty when even one row cannot fit, got %d chars", len(got))
	}
	// Small input is untouched.
	if formatFileHistoryCapped("/p/a.go", rows[:2], hookMaxChars) != formatFileHistory("/p/a.go", rows[:2]) {
		t.Error("cap must not alter output that fits")
	}
}

func TestFormatFileHistoryStripsTerminalEscapes(t *testing.T) {
	out := formatFileHistory("/p/a\x1b[2J.go", []sessions.FileTouch{{SessionUUID: "abcdef123", Title: "t\x1b]0;pwn\x07itle", TS: 1781333200, Tools: []string{"Re\x1b[2Jad"}}})
	if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x07) {
		t.Fatalf("file-history digest carries a control byte: %q", out)
	}
}

// bashFixture builds a cwd holding a few regular files, a directory and an
// outside file, so candidate extraction can be table-tested against real stat.
type bashFixture struct{ cwd, outside string }

func newBashFixture(t *testing.T) bashFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir()) // macOS /var → /private/var
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "p")
	sibling := filepath.Join(root, "p2") // shares the "/p" string prefix
	for _, d := range []string{filepath.Join(cwd, "sub dir"), filepath.Join(cwd, "adir"), sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"a.go", "b.go", "c.go", "d.go", filepath.Join("sub dir", "e.go")} {
		if err := os.WriteFile(filepath.Join(cwd, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(sibling, "o.go")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return bashFixture{cwd: cwd, outside: outside}
}

func TestBashFileCandidates(t *testing.T) {
	f := newBashFixture(t)
	abs := func(rel string) string { return filepath.Join(f.cwd, rel) }
	cases := []struct {
		name, cmd, cwd string
		want           []string
	}{
		{"plain cat", "cat a.go", f.cwd, []string{abs("a.go")}},
		{"dot slash", "cat ./a.go", f.cwd, []string{abs("a.go")}},
		{"double-quoted path with space", `cat "sub dir/e.go"`, f.cwd, []string{abs("sub dir/e.go")}},
		{"single-quoted path with space", `sed -n 1,5p 'sub dir/e.go'`, f.cwd, []string{abs("sub dir/e.go")}},
		{"absolute inside cwd", "cat " + abs("a.go"), f.cwd, []string{abs("a.go")}},
		{"absolute outside cwd skipped", "cat " + f.outside, f.cwd, nil},
		{"dotdot escape skipped", "cat ../p2/o.go", f.cwd, nil},
		{"flags skipped", "grep -n --color=auto foo a.go", f.cwd, []string{abs("a.go")}},
		{"dollar skipped", "cat $HOME/a.go $X", f.cwd, nil},
		{"missing file skipped", "cat nope.go", f.cwd, nil},
		{"directory skipped", "ls adir", f.cwd, nil},
		{"separators trimmed", "(cat a.go;cat b.go)|wc -l", f.cwd, []string{abs("a.go"), abs("b.go")}},
		{"redirect and comma trimmed", "cat a.go>out.txt, b.go:", f.cwd, []string{abs("a.go"), abs("b.go")}},
		{"dedupe keeps first order", "cat b.go a.go b.go ./a.go", f.cwd, []string{abs("b.go"), abs("a.go")}},
		{"fourth file truncated", "cat a.go b.go c.go d.go", f.cwd, []string{abs("a.go"), abs("b.go"), abs("c.go")}},
		{"heredoc body path counts", "cat <<EOF\nsee c.go\nEOF", f.cwd, []string{abs("c.go")}},
		{"empty cwd skips relative", "cat a.go", "", nil},
		{"empty cwd skips absolute too", "cat " + abs("a.go"), "", nil},
		{"no file tokens", "git status", f.cwd, nil},
		{"empty command", "", f.cwd, nil},
	}
	for _, c := range cases {
		cmd := c.cmd
		got := bashFileCandidates(cmd, c.cwd, bashMaxFiles)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: %q → %q, want %q", c.name, cmd, got, c.want)
		}
	}
}

func bashPayload(command, cwd, session string) []byte {
	b, _ := json.Marshal(map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": command},
		"cwd": cwd, "session_id": session,
	})
	return b
}

func hookContext(t *testing.T, out string) string {
	t.Helper()
	var env struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not the hook envelope: %q (%v)", out, err)
	}
	return env.HookSpecificOutput.AdditionalContext
}

func TestFileHistoryHookBashInjectsForExistingFile(t *testing.T) {
	f := newBashFixture(t)
	file := filepath.Join(f.cwd, "a.go")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	dbPath := seedFileHistoryDB(t, file, time.Now().Add(-time.Hour).Unix())
	out := fileHistoryHook(bashPayload("cat a.go | head", f.cwd, "current-9999"), dbPath, 10)
	ctx := hookContext(t, out)
	for _, want := range []string{file, "Fix ranking", "past-111"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("additionalContext missing %q:\n%s", want, ctx)
		}
	}
}

// No candidate → the DB is never opened: a path that does not exist proves it
// (an opened-but-missing DB would also be silent, so also assert no side effect).
func TestFileHistoryHookBashZeroCandidatesDoesNotTouchDB(t *testing.T) {
	f := newBashFixture(t)
	missing := filepath.Join(t.TempDir(), "nope", "db.sqlite")
	for _, cmd := range []string{"git status", "cat nope.go", "cat " + f.outside, "ls adir"} {
		if out := fileHistoryHook(bashPayload(cmd, f.cwd, "s"), missing, 10); out != "" {
			t.Errorf("%q: expected silence, got %q", cmd, out)
		}
	}
	// A DB that would answer if consulted: zero candidates must still be silent.
	dbPath := seedFileHistoryDB(t, filepath.Join(f.cwd, "a.go"), time.Now().Unix())
	if out := fileHistoryHook(bashPayload("git status", f.cwd, "s"), dbPath, 10); out != "" {
		t.Errorf("zero candidates must be silent, got %q", out)
	}
}

func TestFileHistoryHookBashMultiFileSegments(t *testing.T) {
	f := newBashFixture(t)
	a, b := filepath.Join(f.cwd, "a.go"), filepath.Join(f.cwd, "b.go")
	old := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{a, b} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	ts := time.Now().Add(-time.Hour).Unix()
	dbPath := seedFileHistoryDB(t, a, ts)
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`INSERT INTO messages(id, session_uuid, seq, ts, role, content, raw_json) VALUES (2,'past-1111',1,` + itoa(ts) + `,'tool_use','Read y','{}')`,
		`INSERT INTO tool_calls(message_id, tool_name, params_summary) VALUES (2,'Read','y')`,
		`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (2,'past-1111',` + itoa(ts) + `,'file','` + b + `')`,
	} {
		if _, err := d.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	ctx := hookContext(t, fileHistoryHook(bashPayload("diff a.go b.go", f.cwd, "cur"), dbPath, 10))
	for _, want := range []string{"touched " + a, "touched " + b} {
		if !strings.Contains(ctx, want) {
			t.Errorf("missing segment %q:\n%s", want, ctx)
		}
	}
	if n := strings.Count(ctx, "(Details:"); n != 1 {
		t.Errorf("pointer line must appear once, got %d:\n%s", n, ctx)
	}
}

func TestFileHistoryHookBashStalenessGateStillApplies(t *testing.T) {
	f := newBashFixture(t)
	file := filepath.Join(f.cwd, "a.go") // mtime = now, newer than the touch
	dbPath := seedFileHistoryDB(t, file, time.Now().Add(-time.Hour).Unix())
	if out := fileHistoryHook(bashPayload("cat a.go", f.cwd, "cur"), dbPath, 10); out != "" {
		t.Errorf("stale history must not be injected, got %q", out)
	}
	// And the caller's own session is excluded.
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(file, old, old)
	if out := fileHistoryHook(bashPayload("cat a.go", f.cwd, "past-1111"), dbPath, 10); out != "" {
		t.Errorf("own session must be excluded, got %q", out)
	}
}

func TestFileHistoryHookBashCapHolds(t *testing.T) {
	f := newBashFixture(t)
	a := filepath.Join(f.cwd, "a.go")
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(a, old, old)
	dbPath := seedFileHistoryDB(t, a, time.Now().Add(-time.Hour).Unix())
	d, _ := db.Open(dbPath)
	for i := 2; i < 200; i++ {
		id := itoa(int64(i))
		for _, s := range []string{
			`INSERT INTO sessions(uuid, project_path, source_file, ended_at, turn_count, title) VALUES ('s` + id + `','/p','p.jsonl',1,2,'` + strings.Repeat("t", 60) + `')`,
			`INSERT INTO messages(id, session_uuid, seq, ts, role, content, raw_json) VALUES (` + id + `,'s` + id + `',0,1,'tool_use','Edit x','{}')`,
			`INSERT INTO tool_calls(message_id, tool_name, params_summary) VALUES (` + id + `,'Edit','x')`,
			`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (` + id + `,'s` + id + `',1,'file','` + a + `')`,
		} {
			if _, err := d.Exec(s); err != nil {
				t.Fatal(err)
			}
		}
	}
	d.Close()
	out := fileHistoryHook(bashPayload("cat a.go", f.cwd, "cur"), dbPath, 500)
	if out == "" || len([]rune(out)) > hookMaxChars {
		t.Fatalf("want non-empty output within %d chars, got %d", hookMaxChars, len([]rune(out)))
	}
}

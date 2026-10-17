package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/sessions"
)

func TestProjectRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := projectRoot(sub); got != root {
		t.Errorf("projectRoot(subdir) = %q, want repo root %q", got, root)
	}
	noGit := t.TempDir()
	if got := projectRoot(noGit); got != noGit {
		t.Errorf("projectRoot(no-git) = %q, want %q", got, noGit)
	}
}

func TestParseHookCwd(t *testing.T) {
	if got := parseHookCwd([]byte(`{"cwd":"/Users/lin/Herd/clio","session_id":"x"}`)); got != "/Users/lin/Herd/clio" {
		t.Errorf("cwd from hook payload = %q", got)
	}
	if got := parseHookCwd([]byte("not json")); got != "" {
		t.Errorf("garbage => %q, want empty", got)
	}
	if got := parseHookCwd(nil); got != "" {
		t.Errorf("nil => %q, want empty", got)
	}
}

func TestFormatRecallEmptyIsSilent(t *testing.T) {
	if got := formatRecall("/p", sessions.Recall{}); got != "" {
		t.Errorf("empty recall must print nothing, got %q", got)
	}
}

func TestFormatRecallIncludesSections(t *testing.T) {
	r := sessions.Recall{
		Sessions: []sessions.Session{{UUID: "s1", Title: "Activity index", TurnCount: 5, EndedAt: 1781333200}},
		Files:    []sessions.ActivityCount{{Value: "/p/a.go", Count: 3}},
		Commands: []sessions.ActivityCount{{Value: "go test ./...", Count: 7}},
	}
	got := formatRecall("/p", r)
	for _, want := range []string{"/p", "Activity index", "/p/a.go", "go test ./...", "Recent sessions", "touched files", "run commands"} {
		if !strings.Contains(got, want) {
			t.Errorf("digest missing %q\n---\n%s", want, got)
		}
	}
}

func TestFormatRecallTailSectionFirst(t *testing.T) {
	r := sessions.Recall{
		LastAssistant: &sessions.TailExcerpt{SessionUUID: "3d6a1f00-aaaa-bbbb-cccc-dddddddddddd", TS: 1781333200, Text: "Done: shipped X. Next: Y."},
		Sessions:      []sessions.Session{{UUID: "3d6a1f00-aaaa-bbbb-cccc-dddddddddddd", Title: "Ship X", TurnCount: 5, EndedAt: 1781333200}},
	}
	got := formatRecall("/p", r)
	tail := strings.Index(got, "Last session left off")
	recent := strings.Index(got, "Recent sessions")
	if tail < 0 || recent < 0 || tail > recent {
		t.Fatalf("tail section must come before the sessions list\n---\n%s", got)
	}
	for _, want := range []string{"3d6a1f00", formatTS(1781333200), "Done: shipped X. Next: Y."} {
		if !strings.Contains(got, want) {
			t.Errorf("digest missing %q\n---\n%s", want, got)
		}
	}
}

// An excerpt alone (project idle longer than --since) still prints.
func TestFormatRecallTailOnly(t *testing.T) {
	r := sessions.Recall{LastAssistant: &sessions.TailExcerpt{SessionUUID: "s1", TS: 1781333200, Text: "Finished migration"}}
	got := formatRecall("/p", r)
	if !strings.Contains(got, "Finished migration") || strings.Contains(got, "Recent sessions") {
		t.Errorf("tail-only digest wrong:\n%s", got)
	}
}

func TestFormatRecallNoTailWhenNil(t *testing.T) {
	r := sessions.Recall{Sessions: []sessions.Session{{UUID: "s1", Title: "t", TurnCount: 1, EndedAt: 1781333200}}}
	if got := formatRecall("/p", r); strings.Contains(got, "Last session left off") {
		t.Errorf("no excerpt → no section\n---\n%s", got)
	}
}

func TestApplyTailBound(t *testing.T) {
	long := strings.Repeat("驗", 700) + "\n\n  trailing   words"
	r := sessions.Recall{LastAssistant: &sessions.TailExcerpt{Text: long}}
	applyTailBound(&r, 600)
	got := []rune(r.LastAssistant.Text)
	if len(got) != 601 || got[600] != '…' {
		t.Fatalf("want 600 runes + ellipsis, got %d runes ending %q", len(got), string(got[len(got)-1:]))
	}
	r = sessions.Recall{LastAssistant: &sessions.TailExcerpt{Text: "a\n\nb   c"}}
	applyTailBound(&r, 600)
	if r.LastAssistant.Text != "a b c" {
		t.Errorf("whitespace should collapse, got %q", r.LastAssistant.Text)
	}
	r = sessions.Recall{LastAssistant: &sessions.TailExcerpt{Text: "x"}}
	applyTailBound(&r, 0)
	if r.LastAssistant != nil {
		t.Error("bound 0 must drop the section")
	}
	r = sessions.Recall{LastAssistant: &sessions.TailExcerpt{Text: " \n\t "}}
	applyTailBound(&r, 600)
	if r.LastAssistant != nil {
		t.Error("whitespace-only text must drop the section so the digest stays silent")
	}
}

// The SessionStart hook must never see an error: a missing index yields "".
func TestRecallDigestMissingIndexIsSilent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "nowhere"))
	if got := recallDigest(recallHook{}, "/p", "14d", 5, 600, false); got != "" {
		t.Errorf("missing index must be silent, got %q", got)
	}
}

func TestParseHookPayloadSessionID(t *testing.T) {
	h := parseHookPayload([]byte(`{"cwd":"/p","session_id":"abc-123","hook_event_name":"SessionStart","source":"resume"}`))
	if h.Cwd != "/p" || h.SessionID != "abc-123" {
		t.Errorf("payload = %+v", h)
	}
	if h := parseHookPayload(nil); h != (recallHook{}) {
		t.Errorf("no stdin (manual run) => %+v, want zero", h)
	}
}

// On resume/compact the hook payload names the session being opened; the digest
// must recall the session before it, not the current one.
func TestRecallDigestExcludesHookSession(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "clio"), 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(xdg, "clio", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO sessions(uuid, project_path, source_file, ended_at, turn_count, title) VALUES ('prev-0000','/p','a.jsonl',?,2,'Previous work')`, []any{now - 100}},
		{`INSERT INTO sessions(uuid, project_path, source_file, ended_at, turn_count, title) VALUES ('cur-11111','/p','b.jsonl',?,2,'Current work')`, []any{now}},
		{`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES ('prev-0000',0,?,'assistant','prev closing','{}')`, []any{now - 100}},
		{`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES ('cur-11111',0,?,'assistant','current closing','{}')`, []any{now}},
	} {
		if _, err := d.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()

	got := recallDigest(recallHook{SessionID: "cur-11111"}, "/p", "14d", 5, 600, false)
	if !strings.Contains(got, "prev closing") || strings.Contains(got, "current closing") || strings.Contains(got, "Current work") {
		t.Errorf("digest must skip the hook's own session\n---\n%s", got)
	}
	manual := recallDigest(recallHook{}, "/p", "14d", 5, 600, false)
	if !strings.Contains(manual, "current closing") {
		t.Errorf("a manual run (no payload) is unchanged\n---\n%s", manual)
	}
}

func longRecall() sessions.Recall {
	r := sessions.Recall{LastAssistant: &sessions.TailExcerpt{SessionUUID: "s0", TS: 1781333200, Text: strings.Repeat("e", 600)}}
	for i := 0; i < 5; i++ {
		r.Sessions = append(r.Sessions, sessions.Session{UUID: "s", Title: strings.Repeat("t", 60), TurnCount: 1, EndedAt: 1781333200})
		r.Files = append(r.Files, sessions.ActivityCount{Value: "/p/" + strings.Repeat("f", 3000), Count: 1})
		r.Commands = append(r.Commands, sessions.ActivityCount{Value: "cmd", Count: 1})
	}
	return r
}

// Claude Code injects only a 2,000-character preview of hook output over
// 10,000 characters, so the digest stays within recallMaxChars.
func TestFormatRecallStaysUnderCap(t *testing.T) {
	got := formatRecall("/p", longRecall())
	if n := len([]rune(got)); n > recallMaxChars {
		t.Fatalf("digest is %d runes, cap %d", n, recallMaxChars)
	}
	// 5 x 3000-rune paths cannot fit: commands and files go, sessions and the
	// excerpt survive intact.
	for _, gone := range []string{"Recently run commands", "Recently touched files"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q should have been dropped", gone)
		}
	}
	if !strings.Contains(got, "Recent sessions") || !strings.Contains(got, strings.Repeat("e", 600)) {
		t.Errorf("sessions and full excerpt should survive\n%s", got)
	}
	if strings.Count(got, "\n  - ") != 5 {
		t.Errorf("all 5 sessions should survive")
	}
}

// Shrinking goes in a fixed order and stops as soon as the digest fits.
func TestFormatRecallShrinkOrder(t *testing.T) {
	r := longRecall()
	for i := range r.Files {
		r.Files[i].Value = "/p/short.go"
	}
	full := formatRecallCapped("/p", r, 1<<30)
	n := len([]rune(full))

	// Just over the cap: only the commands section goes.
	got := formatRecallCapped("/p", r, n-1)
	if strings.Contains(got, "Recently run commands") || !strings.Contains(got, "Recently touched files") {
		t.Errorf("first step drops commands only\n%s", got)
	}

	noCmds := len([]rune(got))
	got = formatRecallCapped("/p", r, noCmds-1)
	if strings.Contains(got, "Recently touched files") || strings.Count(got, "\n  - ") != 5 {
		t.Errorf("second step drops files only\n%s", got)
	}

	noFiles := len([]rune(got))
	got = formatRecallCapped("/p", r, noFiles-1)
	if strings.Count(got, "\n  - ") != 1 || !strings.Contains(got, strings.Repeat("e", 600)) {
		t.Errorf("third step keeps one session and the full excerpt\n%s", got)
	}

	oneSess := len([]rune(got))
	got = formatRecallCapped("/p", r, oneSess-1)
	if strings.Contains(got, strings.Repeat("e", 201)) || !strings.Contains(got, strings.Repeat("e", 200)+"…") {
		t.Errorf("fourth step cuts the excerpt to 200 runes\n%s", got)
	}
	if r.LastAssistant.Text != strings.Repeat("e", 600) {
		t.Error("capping must not mutate the caller's Recall")
	}
}

// An excerpt far past the cap (--tail-runes set huge) is cut to 200 runes.
func TestFormatRecallHugeExcerpt(t *testing.T) {
	r := sessions.Recall{LastAssistant: &sessions.TailExcerpt{SessionUUID: "s0", TS: 1781333200, Text: strings.Repeat("驗", 20000)}}
	got := formatRecall("/p", r)
	if n := len([]rune(got)); n > recallMaxChars {
		t.Fatalf("digest is %d runes, cap %d", n, recallMaxChars)
	}
	if !strings.Contains(got, strings.Repeat("驗", 200)+"…") || strings.Contains(got, strings.Repeat("驗", 201)) {
		t.Errorf("excerpt should be cut to 200 runes")
	}
}

// When even the minimal digest is too long (an absurd project path), it is
// hard-cut with an ellipsis rather than overflowing.
func TestFormatRecallHardCut(t *testing.T) {
	r := sessions.Recall{Sessions: []sessions.Session{{UUID: "s", Title: "t", TurnCount: 1, EndedAt: 1781333200}}}
	got := formatRecall("/"+strings.Repeat("p", 20000), r)
	rs := []rune(got)
	if len(rs) > recallMaxChars || rs[len(rs)-1] != '…' {
		t.Fatalf("want a hard cut ending in an ellipsis within %d runes, got %d", recallMaxChars, len(rs))
	}
}

func TestFormatRecallStripsTerminalEscapes(t *testing.T) {
	r := sessions.Recall{
		LastAssistant: &sessions.TailExcerpt{SessionUUID: "s1", Text: "left\x1b]52;c;SGVsbG8=\x07 off\x1b[2J"},
		Sessions:      []sessions.Session{{UUID: "s1", Title: "t\x1b]0;pwn\x07itle", TurnCount: 1}},
		Files:         []sessions.ActivityCount{{Value: "/p/a\x1b[2J.go", Count: 1}},
		Commands:      []sessions.ActivityCount{{Value: "echo \x1b]52;c;QQ==\x07hi", Count: 1}},
	}
	got := formatRecall("/p\x1b[2J", r)
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x07) {
		t.Fatalf("digest carries a control byte: %q", got)
	}
	for _, want := range []string{"left off", "title", "/p/a.go", "echo hi"} {
		if !strings.Contains(got, want) {
			t.Errorf("visible text lost, missing %q\n%s", want, got)
		}
	}
}

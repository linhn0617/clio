package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if got := recallDigest("/p", "14d", 5, 600, false); got != "" {
		t.Errorf("missing index must be silent, got %q", got)
	}
}

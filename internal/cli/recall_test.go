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

package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/ask"
)

// On a machine that has not indexed yet, `clio ask` returns a clean empty result
// and exits 0 — it must not error out the way a missing index does elsewhere.
func TestAskMissingIndexIsEmptyNotError(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // no clio db under here
	cmd := newAskCmd()
	cmd.SetArgs([]string{"anything at all"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ask with no index should exit 0, got error: %v", err)
	}
}

// A non-ENOENT failure resolving the index (here: a regular file where the data
// dir should be) must surface as an error, not be reported as empty history.
func TestAskStatErrorSurfaces(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", notADir) // db path resolves under a regular file
	cmd := newAskCmd()
	cmd.SetArgs([]string{"a question"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("ask should surface a non-ENOENT stat error, not report empty history")
	}
}

func TestWriteAnswerMarksHitsAndCites(t *testing.T) {
	var buf bytes.Buffer
	ans := ask.Answer{
		Question: "how did we fix auth",
		Groups: []ask.EvidenceGroup{{
			SessionUUID: "abcdef1234567890",
			Title:       "Auth fix",
			Project:     "/home/me/proj",
			Excerpts: []ask.Excerpt{
				{Seq: 0, Role: "user", Text: "we have an auth problem", IsHit: true},
				{Seq: 1, Role: "assistant", Text: "refresh the token", IsHit: false},
			},
		}},
	}
	writeAnswer(&buf, ans)
	out := buf.String()

	if !strings.Contains(out, "abcdef12") || !strings.Contains(out, "Auth fix") || !strings.Contains(out, "proj") {
		t.Fatalf("citation (id/title/project) missing: %q", out)
	}
	if !strings.Contains(out, "»") {
		t.Fatalf("hit marker missing: %q", out)
	}
	if !strings.Contains(out, "we have an auth problem") || !strings.Contains(out, "refresh the token") {
		t.Fatalf("excerpt text missing: %q", out)
	}
}

func TestWriteAnswerEmptyIsFriendly(t *testing.T) {
	var buf bytes.Buffer
	writeAnswer(&buf, ask.Answer{Question: "anything"})
	if !strings.Contains(buf.String(), "no relevant history") {
		t.Fatalf("empty-answer message missing: %q", buf.String())
	}
}

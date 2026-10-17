package tui

import (
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/ask"
	"github.com/linhn0617/clio/internal/sessions"
)

const hostile = "a\x1b]52;c;SGVsbG8=\x07b\x1b[2Jc\x1b]0;pwn\x07"

func noControl(s string) bool {
	return !strings.ContainsRune(s, 0x1b) && !strings.ContainsRune(s, 0x07)
}

// Indexed text can carry terminal escapes; the TUI would replay them into the
// user's terminal (clipboard write, title change, screen clear) if left in.
func TestOneLineStripsTerminalEscapes(t *testing.T) {
	if got := oneLine(hostile); got != "abc" {
		t.Fatalf("oneLine = %q, want %q", got, "abc")
	}
}

func TestRenderPreviewStripsTerminalEscapes(t *testing.T) {
	out := renderPreview([]sessions.Message{{Seq: 1, Role: "assistant", Content: hostile}}, nil, -1)
	if !noControl(out) || !strings.Contains(out, "abc") {
		t.Fatalf("preview not clean: %q", out)
	}
}

func TestAskExcerptsStripTerminalEscapes(t *testing.T) {
	v := askView{
		query: "q", answered: "q",
		groups: []ask.EvidenceGroup{{
			SessionUUID: "abcdef123", Title: hostile,
			Excerpts: []ask.Excerpt{{Role: "user", Text: hostile}},
		}},
	}
	out := v.renderExcerpts()
	if !noControl(out) || !strings.Contains(out, "abc") {
		t.Fatalf("excerpts not clean: %q", out)
	}
}

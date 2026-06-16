package tui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// A narrow terminal must not overflow: the right pane has to keep at least 2
// cells so a width-2 CJK glyph fits instead of being padded past the edge.
func TestMasterDetailNarrowTerminalNoOverflow(t *testing.T) {
	out := masterDetail(26, 10,
		func(w, h int) string { return "list" },
		"驗證資料庫遷移流程設計", "status")
	for _, line := range strings.Split(out, "\n") {
		if width := runewidth.StringWidth(line); width > 26 {
			t.Fatalf("line exceeds terminal width 26 (got %d): %q", width, line)
		}
	}
}

// Even a 1-column terminal must not overflow: the status line's "…" ellipsis is
// width 2, so it needs an empty tail when there is no room for it.
func TestMasterDetailWidth1NoOverflow(t *testing.T) {
	out := masterDetail(1, 6,
		func(w, h int) string { return "list" },
		"preview", "12 results · ↑/↓ navigate · esc quit")
	for _, line := range strings.Split(out, "\n") {
		if width := runewidth.StringWidth(line); width > 1 {
			t.Fatalf("width-1 terminal overflowed (got %d): %q", width, line)
		}
	}
}

// A tiny terminal must not explode into a 24-line default render.
func TestMasterDetailTinyHeight(t *testing.T) {
	out := masterDetail(80, 2,
		func(w, h int) string { return "x" },
		"preview", "status")
	if n := strings.Count(out, "\n") + 1; n > 2 {
		t.Fatalf("height 2 should render at most 2 lines, got %d:\n%q", n, out)
	}
}

package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// masterDetail composes the shared two-pane dashboard layout used by every tab: a
// left list, a right detail/preview pane, and a status line beneath. renderList is
// given the exact width and height of the left pane; preview and status are
// already-rendered strings. The status line is truncated to the terminal width.
func masterDetail(width, height int, renderList func(w, h int) string, preview, status string) string {
	w := width
	if w <= 0 {
		w = 80
	}
	h := height
	if h <= 0 {
		h = 24
	}
	bodyH := max(h-1, 1) // reserve the status line
	leftW := w / 3
	leftW = max(leftW, 24)
	leftW = min(leftW, w-2)
	leftW = max(leftW, 1)
	rightW := max(w-leftW-1, 1)

	box := func(width int) lipgloss.Style {
		return lipgloss.NewStyle().Width(width).Height(bodyH).MaxHeight(bodyH)
	}
	divider := strings.TrimRight(strings.Repeat("│\n", bodyH), "\n")
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		box(leftW).Render(renderList(leftW, bodyH)),
		divider,
		box(rightW).Render(preview),
	)
	return body + "\n" + runewidth.Truncate(status, w, "…")
}

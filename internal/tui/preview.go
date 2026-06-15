package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/sessions"
)

// previewMessageLimit bounds how many messages the (whole-session) preview loads.
const previewMessageLimit = 500

// previewHitBefore/After bound the dialogue window the Search preview loads around
// a hit: a little leading context so the hit sits near the top of the pane, and a
// generous trailing window the pane truncates as needed.
const (
	previewHitBefore = 3
	previewHitAfter  = 60
)

// previewMatchMarker prefixes the selected list row and the matched preview
// message, so both stand out even on terminals without color.
const previewMatchMarker = "▸ "

// previewLoadedMsg carries the messages loaded for a preview pane. routeAll fans
// every message out to all views, and Search and Browse share this type, so it is
// keyed by owner (the requesting view) and gen (that view's preview generation):
// a view applies a load only if it owns it and no newer load has superseded it.
type previewLoadedMsg struct {
	owner tab
	gen   int
	msgs  []sessions.Message
	err   error
}

// loadSessionPreview reads a session's dialogue messages from the start, for the
// Browse preview (which previews a whole session, not a specific hit). It returns
// a nil command when there is nothing to load; the query runs under ctx so
// quitting cancels it in flight.
func loadSessionPreview(ctx context.Context, database *db.DB, sessionUUID string, owner tab, gen int) tea.Cmd {
	if database == nil || sessionUUID == "" {
		return nil
	}
	return func() tea.Msg {
		msgs, _, err := sessions.GetMessages(orBackground(ctx), database, sessionUUID, 0, previewMessageLimit, false)
		return previewLoadedMsg{owner: owner, gen: gen, msgs: msgs, err: err}
	}
}

// loadHitPreview reads a dialogue window centred on a search hit (by in-session
// seq), so the Search preview shows the selected hit in context even when it is
// deep in a long session or one of several hits in the same session.
func loadHitPreview(ctx context.Context, database *db.DB, sessionUUID string, hitSeq int, owner tab, gen int) tea.Cmd {
	if database == nil || sessionUUID == "" {
		return nil
	}
	return func() tea.Msg {
		msgs, err := sessions.GetWindow(orBackground(ctx), database, sessionUUID, hitSeq, previewHitBefore, previewHitAfter, false)
		return previewLoadedMsg{owner: owner, gen: gen, msgs: msgs, err: err}
	}
}

// orBackground defaults a nil context to context.Background, so views built as
// bare struct literals (in tests) keep working without an explicit context.
func orBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// renderPreview renders a session's messages for the right-hand pane, marking the
// message whose in-session seq is markSeq (the selected hit). Pass markSeq < 0 to
// mark nothing (e.g. the Browse preview, which has no specific hit).
func renderPreview(msgs []sessions.Message, loadErr error, markSeq int) string {
	if loadErr != nil {
		return "preview error: " + loadErr.Error()
	}
	if len(msgs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range msgs {
		marker := "  "
		if m.Seq == markSeq {
			marker = previewMatchMarker
		}
		b.WriteString(marker + m.Role + "\n")
		b.WriteString(m.Content + "\n\n")
	}
	return b.String()
}

// oneLine collapses text onto a single line for list rows.
func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
}

// shortID abbreviates a session UUID for list rows.
func shortID(uuid string) string {
	if r := []rune(uuid); len(r) > 8 {
		return string(r[:8])
	}
	return uuid
}

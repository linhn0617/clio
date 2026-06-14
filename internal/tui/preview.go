package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/sessions"
)

// previewMessageLimit bounds how many messages the preview pane loads for a session.
const previewMessageLimit = 500

// previewMatchMarker prefixes the selected list row and the matched preview
// message, so both stand out even on terminals without color.
const previewMatchMarker = "▸ "

// previewLoadedMsg carries the messages loaded for a session preview, keyed by
// sessionUUID so a load the selection has moved past is dropped.
type previewLoadedMsg struct {
	sessionUUID string
	msgs        []sessions.Message
	err         error
}

// loadSessionPreview reads a session's dialogue messages for the preview pane,
// shared by the Search and Browse views. It returns a nil command when there is
// nothing to load.
func loadSessionPreview(database *db.DB, sessionUUID string) tea.Cmd {
	if database == nil || sessionUUID == "" {
		return nil
	}
	return func() tea.Msg {
		msgs, _, err := sessions.GetMessages(context.Background(), database, sessionUUID, 0, previewMessageLimit, false)
		return previewLoadedMsg{sessionUUID: sessionUUID, msgs: msgs, err: err}
	}
}

// renderPreview renders a session's messages for the right-hand pane, marking the
// first line that matched the query (when query is non-empty).
func renderPreview(msgs []sessions.Message, loadErr error, query string) string {
	if loadErr != nil {
		return "preview error: " + loadErr.Error()
	}
	if len(msgs) == 0 {
		return ""
	}
	match := firstPreviewMatch(msgs, query)
	var b strings.Builder
	for i, m := range msgs {
		marker := "  "
		if i == match {
			marker = previewMatchMarker
		}
		b.WriteString(marker + m.Role + "\n")
		b.WriteString(m.Content + "\n\n")
	}
	return b.String()
}

// firstPreviewMatch returns the index of the first message whose content contains
// a query term (case-insensitive), or -1 when none match.
func firstPreviewMatch(msgs []sessions.Message, query string) int {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return -1
	}
	for i, m := range msgs {
		lc := strings.ToLower(m.Content)
		for _, t := range terms {
			if strings.Contains(lc, t) {
				return i
			}
		}
	}
	return -1
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

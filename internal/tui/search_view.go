package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/search"
	"github.com/linhn0617/clio/internal/sessions"
)

// searchDebounce delays the query after the last keystroke so live search doesn't
// run on every character.
const searchDebounce = 200 * time.Millisecond

// previewMessageLimit bounds how many messages the preview pane loads for the
// selected session.
const previewMessageLimit = 500

// previewMatchMarker prefixes the preview message that matched the query, so the
// hit stands out even on terminals without color.
const previewMatchMarker = "▸ "

// searchHit is one result row the search view renders (a thin view of search.Result).
type searchHit struct {
	sessionUUID string
	project     string
	role        string
	ts          int64
	snippet     string
}

// searchView is the live-search tab: a query, its results, the selection, and a
// preview of the selected hit's session.
type searchView struct {
	db            *db.DB
	width, height int
	query         string
	gen           int // bumps on each query change; stale ticks/results are dropped
	results       []searchHit
	selected      int
	err           error
	previewMsgs   []sessions.Message
	previewErr    error
}

type searchDebounceMsg struct{ gen int }

type searchResultsMsg struct {
	gen     int
	results []searchHit
	err     error
}

// previewLoadedMsg carries the messages loaded for a session's preview. It is
// keyed by sessionUUID so a load that finishes after the selection moved on is
// dropped.
type previewLoadedMsg struct {
	sessionUUID string
	msgs        []sessions.Message
	err         error
}

// scheduleSearch bumps the generation and starts the debounce timer; only the
// matching searchDebounceMsg will fire the query, so earlier keystrokes are dropped.
func (v searchView) scheduleSearch() (searchView, tea.Cmd) {
	v.gen++
	g := v.gen
	return v, tea.Tick(searchDebounce, func(time.Time) tea.Msg { return searchDebounceMsg{gen: g} })
}

func (v searchView) Update(msg tea.Msg) (searchView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
	case searchDebounceMsg:
		if msg.gen == v.gen { // no newer keystroke since this was scheduled
			return v, v.runSearch(v.gen)
		}
	case searchResultsMsg:
		if msg.gen == v.gen { // ignore results from a superseded query
			v.results, v.err, v.selected = msg.results, msg.err, 0
			v.previewMsgs, v.previewErr = nil, nil
			return v, v.loadPreview()
		}
	case previewLoadedMsg:
		if msg.sessionUUID == v.selectedSession() { // ignore a load the selection moved past
			v.previewMsgs, v.previewErr = msg.msgs, msg.err
		}
	case tea.KeyMsg:
		// The query input is focused: arrows navigate; printable keys (including
		// j/k/q) are query text.
		switch msg.String() {
		case "up":
			if v.selected > 0 {
				v.selected--
				return v, v.loadPreview()
			}
		case "down":
			if v.selected < len(v.results)-1 {
				v.selected++
				return v, v.loadPreview()
			}
		case "backspace":
			if r := []rune(v.query); len(r) > 0 {
				v.query = string(r[:len(r)-1])
				return v.scheduleSearch()
			}
		default:
			if msg.Type == tea.KeyRunes {
				v.query += string(msg.Runes)
				return v.scheduleSearch()
			}
		}
	}
	return v, nil
}

// selectedSession is the session UUID of the current selection, or "" when there
// is no selection.
func (v searchView) selectedSession() string {
	if v.selected >= 0 && v.selected < len(v.results) {
		return v.results[v.selected].sessionUUID
	}
	return ""
}

// runSearch queries the index for the current query and returns the hits tagged
// with generation g (so a stale result can be dropped).
func (v searchView) runSearch(g int) tea.Cmd {
	q, database := v.query, v.db
	return func() tea.Msg {
		if database == nil || strings.TrimSpace(q) == "" {
			return searchResultsMsg{gen: g}
		}
		res, err := search.Search(context.Background(), database, search.Options{Query: q, Limit: 50})
		if err != nil {
			return searchResultsMsg{gen: g, err: err}
		}
		hits := make([]searchHit, len(res))
		for i, r := range res {
			hits[i] = searchHit{
				sessionUUID: r.SessionUUID,
				project:     r.ProjectPath,
				role:        r.Role,
				ts:          r.TS,
				snippet:     r.Snippet,
			}
		}
		return searchResultsMsg{gen: g, results: hits}
	}
}

// loadPreview reads the selected session's messages for the preview pane. It
// returns nil when there is nothing to preview.
func (v searchView) loadPreview() tea.Cmd {
	sess, database := v.selectedSession(), v.db
	if database == nil || sess == "" {
		return nil
	}
	return func() tea.Msg {
		msgs, _, err := sessions.GetMessages(context.Background(), database, sess, 0, previewMessageLimit, false)
		return previewLoadedMsg{sessionUUID: sess, msgs: msgs, err: err}
	}
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

// View renders the master-detail layout: the results list on the left, the
// session preview on the right, and a status line beneath.
func (v searchView) View() string {
	w, h := v.width, v.height
	if w <= 0 {
		w = 80
	}
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
		box(leftW).Render(v.renderList(leftW, bodyH)),
		divider,
		box(rightW).Render(v.renderPreview()),
	)
	return body + "\n" + v.renderStatus(w)
}

func (v searchView) renderList(w, h int) string {
	if len(v.results) == 0 {
		if strings.TrimSpace(v.query) == "" {
			return "Type to search…"
		}
		return "No matches."
	}
	var lines []string
	for i, r := range v.results {
		if i >= h {
			break
		}
		marker := "  "
		if i == v.selected {
			marker = previewMatchMarker
		}
		lines = append(lines, runewidth.Truncate(marker+oneLine(r.snippet), w, "…"))
	}
	return strings.Join(lines, "\n")
}

func (v searchView) renderPreview() string {
	if v.previewErr != nil {
		return "preview error: " + v.previewErr.Error()
	}
	if len(v.previewMsgs) == 0 {
		return ""
	}
	match := firstPreviewMatch(v.previewMsgs, v.query)
	var b strings.Builder
	for i, m := range v.previewMsgs {
		marker := "  "
		if i == match {
			marker = previewMatchMarker
		}
		b.WriteString(marker + m.Role + "\n")
		b.WriteString(m.Content + "\n\n")
	}
	return b.String()
}

func (v searchView) renderStatus(w int) string {
	switch {
	case v.err != nil:
		return runewidth.Truncate("⚠ "+v.err.Error(), w, "…")
	case v.previewErr != nil:
		return runewidth.Truncate("⚠ preview: "+v.previewErr.Error(), w, "…")
	default:
		s := fmt.Sprintf("%d results · ↑/↓ navigate · tab switch view · esc quit", len(v.results))
		return runewidth.Truncate(s, w, "…")
	}
}

// oneLine collapses a snippet onto a single line for the results list.
func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
}

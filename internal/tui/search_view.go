package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/search"
	"github.com/linhn0617/clio/internal/sessions"
)

// searchDebounce delays the query after the last keystroke so live search doesn't
// run on every character.
const searchDebounce = 200 * time.Millisecond

// searchHit is one result row the search view renders (a thin view of search.Result).
type searchHit struct {
	sessionUUID string
	seq         int
	project     string
	role        string
	ts          int64
	snippet     string
}

// searchView is the live-search tab: a query, its results, the selection, and a
// preview of the selected hit's session.
type searchView struct {
	db            *db.DB
	ctx           context.Context
	width, height int
	query         string
	gen           int  // bumps on each query change; stale ticks/results are dropped
	searching     bool // a debounced query is pending; the list shows "Searching…"
	results       []searchHit
	selected      int
	err           error
	previewGen    int // bumps on each preview load; stale preview responses are dropped
	previewMsgs   []sessions.Message
	previewErr    error
}

type searchDebounceMsg struct{ gen int }

type searchResultsMsg struct {
	gen     int
	results []searchHit
	err     error
}

// scheduleSearch bumps the generation and starts the debounce timer; only the
// matching searchDebounceMsg will fire the query, so earlier keystrokes are dropped.
func (v searchView) scheduleSearch() (searchView, tea.Cmd) {
	v.gen++
	// The query changed: drop the previous query's hits and preview now so the UI
	// never shows or navigates results that no longer match the visible query.
	v.results, v.previewMsgs, v.previewErr, v.selected = nil, nil, nil, 0
	v.searching = true
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
			v.searching = false
			return v.loadPreview()
		}
	case previewLoadedMsg:
		if msg.owner == tabSearch && msg.gen == v.previewGen { // ours, and not superseded
			v.previewMsgs, v.previewErr = msg.msgs, msg.err
		}
	case tea.KeyMsg:
		// The query input is focused: arrows navigate; printable keys (including
		// j/k/q) are query text.
		switch msg.String() {
		case "up":
			if v.selected > 0 {
				v.selected--
				return v.selectPreview()
			}
		case "down":
			if v.selected < len(v.results)-1 {
				v.selected++
				return v.selectPreview()
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
	q, database, ctx := v.query, v.db, orBackground(v.ctx)
	return func() tea.Msg {
		if database == nil || strings.TrimSpace(q) == "" {
			return searchResultsMsg{gen: g}
		}
		res, err := search.Search(ctx, database, search.Options{Query: q, Limit: 50})
		if err != nil {
			return searchResultsMsg{gen: g, err: err}
		}
		hits := make([]searchHit, len(res))
		for i, r := range res {
			hits[i] = searchHit{
				sessionUUID: r.SessionUUID,
				seq:         r.Seq,
				project:     r.ProjectPath,
				role:        r.Role,
				ts:          r.TS,
				snippet:     r.Snippet,
			}
		}
		return searchResultsMsg{gen: g, results: hits}
	}
}

// loadPreview reads a dialogue window around the selected hit for the preview
// pane. It bumps the preview generation so a slower response for an earlier hit
// is dropped, and returns the updated view alongside the command.
func (v searchView) loadPreview() (searchView, tea.Cmd) {
	v.previewGen++
	if v.selected < 0 || v.selected >= len(v.results) {
		return v, nil
	}
	h := v.results[v.selected]
	return v, loadHitPreview(v.ctx, v.db, h.sessionUUID, h.seq, tabSearch, v.previewGen)
}

// selectedHitSeq is the in-session seq of the selected hit, marked in the preview;
// -1 when there is no selection.
func (v searchView) selectedHitSeq() int {
	if v.selected >= 0 && v.selected < len(v.results) {
		return v.results[v.selected].seq
	}
	return -1
}

// selectPreview drops the previous hit's preview before loading the new
// selection's, so the preview pane never shows the wrong conversation.
func (v searchView) selectPreview() (searchView, tea.Cmd) {
	v.previewMsgs, v.previewErr = nil, nil
	return v.loadPreview()
}

// View renders the master-detail layout: the results list on the left, the
// session preview on the right, and a status line beneath.
func (v searchView) View() string {
	header := "› " + v.query
	body := masterDetail(v.width, v.height-1, v.renderList,
		renderPreview(v.previewMsgs, v.previewErr, v.selectedHitSeq()), v.statusLine())
	return header + "\n" + body
}

func (v searchView) renderList(w, h int) string {
	if len(v.results) == 0 {
		switch {
		case v.searching:
			return "Searching…"
		case strings.TrimSpace(v.query) == "":
			return "Type to search…"
		default:
			return "No matches."
		}
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

func (v searchView) statusLine() string {
	switch {
	case v.err != nil:
		return "⚠ " + v.err.Error()
	case v.previewErr != nil:
		return "⚠ preview: " + v.previewErr.Error()
	default:
		return fmt.Sprintf("%d results · ↑/↓ navigate · tab switch view · esc quit", len(v.results))
	}
}

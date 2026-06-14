package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/search"
)

// searchDebounce delays the query after the last keystroke so live search doesn't
// run on every character.
const searchDebounce = 200 * time.Millisecond

// searchHit is one result row the search view renders (a thin view of search.Result).
type searchHit struct {
	sessionUUID string
	project     string
	role        string
	ts          int64
	snippet     string
}

// searchView is the live-search tab: a query, its results, and the selection.
type searchView struct {
	db       *db.DB
	query    string
	gen      int // bumps on each query change; stale ticks/results are dropped
	results  []searchHit
	selected int
	err      error
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
	g := v.gen
	return v, tea.Tick(searchDebounce, func(time.Time) tea.Msg { return searchDebounceMsg{gen: g} })
}

func (v searchView) Update(msg tea.Msg) (searchView, tea.Cmd) {
	switch msg := msg.(type) {
	case searchDebounceMsg:
		if msg.gen == v.gen { // no newer keystroke since this was scheduled
			return v, v.runSearch(v.gen)
		}
	case searchResultsMsg:
		if msg.gen == v.gen { // ignore results from a superseded query
			v.results, v.err, v.selected = msg.results, msg.err, 0
		}
	case tea.KeyMsg:
		// The query input is focused: arrows navigate; printable keys (including
		// j/k/q) are query text.
		switch msg.String() {
		case "up":
			if v.selected > 0 {
				v.selected--
			}
		case "down":
			if v.selected < len(v.results)-1 {
				v.selected++
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

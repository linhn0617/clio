package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/sessions"
)

// newTest builds a root model for tests with a background context.
func newTest(database *db.DB) Model { return New(context.Background(), database, "") }

// visibleWindow keeps the selected row inside a pane of height h, clamped to ends.
func TestVisibleWindow(t *testing.T) {
	if s, e := visibleWindow(0, 5, 10); s != 0 || e != 5 {
		t.Fatalf("fits-in-pane should show all: got [%d,%d)", s, e)
	}
	s, e := visibleWindow(25, 30, 10)
	if !(s <= 25 && 25 < e) || e-s != 10 {
		t.Fatalf("selected 25 should be inside a size-10 window: got [%d,%d)", s, e)
	}
	if s, _ := visibleWindow(29, 30, 10); s != 20 {
		t.Fatalf("end clamp: start=%d want 20", s)
	}
	if s, _ := visibleWindow(0, 30, 10); s != 0 {
		t.Fatalf("start clamp: start=%d want 0", s)
	}
	if s, e := visibleWindow(0, 0, 10); s != 0 || e != 0 {
		t.Fatalf("empty list: got [%d,%d)", s, e)
	}
	if s, e := visibleWindow(3, 30, 0); s != 0 || e != 0 {
		t.Fatalf("zero height: got [%d,%d)", s, e)
	}
}

// New threads the context into every sub-view so their queries are cancellable.
func TestNewThreadsContext(t *testing.T) {
	ctx := context.Background()
	m := New(ctx, nil, "")
	if m.search.ctx != ctx || m.browse.ctx != ctx || m.activity.ctx != ctx || m.ask.ctx != ctx {
		t.Fatal("New should thread the context into every sub-view")
	}
}

func step(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

func key(s tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: s} }
func runes(s string) tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// isQuit reports whether a command resolves to tea.Quit. Only call it on commands
// expected to be a quit (tea.Quit resolves instantly).
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestRootTabNavigation(t *testing.T) {
	m := newTest(nil)
	if m.active != tabSearch {
		t.Fatalf("default tab should be Search, got %d", m.active)
	}
	m = step(t, m, key(tea.KeyTab))
	if m.active != tabBrowse {
		t.Fatalf("Tab should advance to Browse, got %d", m.active)
	}
	m = step(t, m, key(tea.KeyShiftTab))
	if m.active != tabSearch {
		t.Fatalf("Shift-Tab should return to Search, got %d", m.active)
	}
	// Tab wraps around at the end, regardless of input focus.
	for range int(tabCount) {
		m = step(t, m, key(tea.KeyTab))
	}
	if m.active != tabSearch {
		t.Fatalf("Tab should wrap back to Search, got %d", m.active)
	}
}

func TestRootViewMarksActiveTab(t *testing.T) {
	m := newTest(nil)
	if !strings.Contains(m.View(), "[Search]") {
		t.Fatalf("view should mark Search active: %q", m.View())
	}
	m = step(t, m, key(tea.KeyTab))
	if v := m.View(); !strings.Contains(v, "[Browse]") || strings.Contains(v, "[Search]") {
		t.Fatalf("after Tab, Browse should be the active tab (not Search): %q", v)
	}
}

// Ctrl-C and Esc quit from any tab, focused or not.
func TestRootGlobalQuit(t *testing.T) {
	tabs := []Model{newTest(nil), step(t, newTest(nil), key(tea.KeyTab))} // Search, Browse
	for _, m := range tabs {
		for _, k := range []tea.Msg{key(tea.KeyCtrlC), key(tea.KeyEsc)} {
			if _, cmd := m.Update(k); !isQuit(cmd) {
				t.Fatalf("%v should quit on tab %d", k, m.active)
			}
		}
	}
}

// 'q' quits only on a list tab; on an input tab it is query text.
func TestRootFocusAwareQuit(t *testing.T) {
	// Search (input focused): 'q' is query text, not a quit.
	m := step(t, newTest(nil), runes("q"))
	if m.active != tabSearch || m.search.query != "q" {
		t.Fatalf("'q' on Search should be query text; active=%d query=%q", m.active, m.search.query)
	}
	// Browse (no input): 'q' quits.
	mb := step(t, newTest(nil), key(tea.KeyTab))
	if _, cmd := mb.Update(runes("q")); !isQuit(cmd) {
		t.Fatal("'q' on the Browse tab should quit")
	}
}

// Number keys jump tabs only when no input is focused.
func TestRootDigitsFocusAware(t *testing.T) {
	// Browse (no input): '3' jumps to Activity.
	mb := step(t, step(t, newTest(nil), key(tea.KeyTab)), runes("3"))
	if mb.active != tabActivity {
		t.Fatalf("'3' on a list tab should select Activity, got %d", mb.active)
	}
	// Search (input focused): '3' is query text, tab unchanged.
	ms := step(t, newTest(nil), runes("3"))
	if ms.active != tabSearch || ms.search.query != "3" {
		t.Fatalf("'3' on Search should be query text; active=%d query=%q", ms.active, ms.search.query)
	}
}

// Non-global keys reach the active sub-view.
func TestRootRoutesKeysToActiveView(t *testing.T) {
	m := step(t, newTest(nil), runes("auth"))
	if m.search.query != "auth" {
		t.Fatalf("typing should reach the search view: %q", m.search.query)
	}
	mb := newTest(nil)
	mb.browse.sessions = []sessions.Session{{UUID: "s1"}, {UUID: "s2"}}
	mb.active = tabBrowse
	mb = step(t, mb, runes("j"))
	if mb.browse.selected != 1 {
		t.Fatalf("j on Browse should move selection, got %d", mb.browse.selected)
	}
}

// Async/data messages route to their view regardless of the active tab.
func TestRootRoutesDataToAllViews(t *testing.T) {
	m := newTest(nil) // active Search
	m = step(t, m, browseLoadedMsg{sessions: []sessions.Session{{UUID: "s1"}}})
	if len(m.browse.sessions) != 1 {
		t.Fatalf("data messages should route to their view regardless of active tab: %+v", m.browse.sessions)
	}
}

// The window size is recorded and forwarded to sub-views minus the tab-bar row.
func TestRootForwardsWindowSize(t *testing.T) {
	m := step(t, newTest(nil), tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 || m.height != 40 {
		t.Fatalf("root should record size: %dx%d", m.width, m.height)
	}
	if m.search.height != 39 {
		t.Fatalf("sub-views should be sized below the tab bar, got %d", m.search.height)
	}
}

// A real tiny terminal height clamps to ≥1 per sub-view instead of underflowing
// to 0 (which masterDetail would treat as unsized and explode to 24 lines).
func TestRootClampsTinyHeight(t *testing.T) {
	m := step(t, newTest(nil), tea.WindowSizeMsg{Width: 80, Height: 1})
	if m.search.height != 1 {
		t.Fatalf("sub-view height should clamp to 1 at terminal height 1, got %d", m.search.height)
	}
}

// A narrow terminal must not let the tab bar overflow the terminal width.
func TestRootTabBarNarrowNoOverflow(t *testing.T) {
	m := step(t, newTest(nil), tea.WindowSizeMsg{Width: 20, Height: 24})
	line := strings.SplitN(m.View(), "\n", 2)[0]
	if w := runewidth.StringWidth(line); w > 20 {
		t.Fatalf("tab bar exceeds terminal width 20 (got %d): %q", w, line)
	}
}

// Even a 1-column terminal must not overflow: the "…" ellipsis is itself
// width 2, so the tab bar needs an empty tail there (like masterDetail).
func TestRootTabBarWidth1NoOverflow(t *testing.T) {
	m := step(t, newTest(nil), tea.WindowSizeMsg{Width: 1, Height: 24})
	line := strings.SplitN(m.View(), "\n", 2)[0]
	if w := runewidth.StringWidth(line); w > 1 {
		t.Fatalf("tab bar exceeds terminal width 1 (got %d): %q", w, line)
	}
}

// The root view includes the active sub-view's rendering.
func TestRootViewIncludesActiveView(t *testing.T) {
	m := newTest(nil)
	m.active = tabBrowse
	m.browse.loaded = true // empty index → "No sessions."
	if !strings.Contains(m.View(), "No sessions") {
		t.Fatalf("root view should include the active sub-view: %q", m.View())
	}
}

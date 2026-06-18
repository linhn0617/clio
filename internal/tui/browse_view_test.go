package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/linhn0617/clio/internal/sessions"
)

// The browse status line surfaces a list-load error instead of crashing.
func TestBrowseViewStatusShowsError(t *testing.T) {
	v := browseView{width: 80, height: 24, err: errors.New("list boom")}
	if !strings.Contains(v.View(), "list boom") {
		t.Fatalf("status should surface the list error: %q", v.View())
	}
}

// renderList scrolls so the selected row stays visible past the pane height.
func TestBrowseViewListScrollsToSelection(t *testing.T) {
	var ss []sessions.Session
	for i := range 30 {
		ss = append(ss, sessions.Session{UUID: fmt.Sprintf("s%02d", i), Title: fmt.Sprintf("session%02d", i)})
	}
	v := browseView{sessions: ss, selected: 25}
	out := v.renderList(80, 8)
	if !strings.Contains(out, "session25") {
		t.Fatalf("the selected row should be visible past the pane height: %q", out)
	}
	if strings.Contains(out, "session00") {
		t.Fatalf("rows above the scroll window should not render: %q", out)
	}
}

func bUpdate(t *testing.T, v browseView, msg tea.Msg) (browseView, tea.Cmd) {
	t.Helper()
	return v.Update(msg)
}

// load reads recent sessions into the list.
func TestBrowseViewLoad(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addSession(t, d, "s2", "/p")
	v := browseView{db: d}
	msg := v.load()()
	bl, ok := msg.(browseLoadedMsg)
	if !ok {
		t.Fatalf("load should emit browseLoadedMsg, got %T", msg)
	}
	if bl.err != nil || len(bl.sessions) != 2 {
		t.Fatalf("load result wrong: %+v err=%v", bl.sessions, bl.err)
	}
}

// Loaded sessions populate the list and schedule a preview of the first.
func TestBrowseViewLoadedPopulatesAndPreviews(t *testing.T) {
	v := browseView{db: testDB(t)}
	v, cmd := bUpdate(t, v, browseLoadedMsg{sessions: []sessions.Session{{UUID: "s1"}, {UUID: "s2"}}})
	if len(v.sessions) != 2 || v.selected != 0 || !v.loaded {
		t.Fatalf("sessions not populated: %+v", v)
	}
	if cmd == nil {
		t.Fatal("loading sessions should schedule a preview of the first")
	}
}

// j/k and arrows move the selection (clamped) and schedule a preview load.
func TestBrowseViewNavigation(t *testing.T) {
	v := browseView{db: testDB(t), sessions: []sessions.Session{{UUID: "s1"}, {UUID: "s2"}, {UUID: "s3"}}}
	v, cmd := bUpdate(t, v, runes("j"))
	if v.selected != 1 {
		t.Fatalf("'j' should move down, selected = %d", v.selected)
	}
	if cmd == nil {
		t.Fatal("moving should schedule a preview load")
	}
	v, _ = bUpdate(t, v, key(tea.KeyDown))
	v, _ = bUpdate(t, v, key(tea.KeyDown)) // clamp at end
	if v.selected != 2 {
		t.Fatalf("selection should clamp at last, got %d", v.selected)
	}
	for range 5 {
		v, _ = bUpdate(t, v, runes("k"))
	}
	if v.selected != 0 {
		t.Fatalf("'k' should move up and clamp at 0, got %d", v.selected)
	}
}

// Moving the selection clears the previous session's preview before loading the
// new one, so the preview pane never shows the wrong conversation.
func TestBrowseViewSelectionClearsStalePreview(t *testing.T) {
	v := browseView{
		sessions:    []sessions.Session{{UUID: "s1"}, {UUID: "s2"}},
		previewMsgs: []sessions.Message{{Content: "old session"}},
	}
	v, _ = bUpdate(t, v, runes("j"))
	if len(v.previewMsgs) != 0 {
		t.Fatal("changing selection should clear the stale preview before the new load")
	}
}

// Preview results for the current generation populate the pane; a stale
// generation, or a preview owned by another view, is ignored.
func TestBrowseViewPreviewResults(t *testing.T) {
	v := browseView{sessions: []sessions.Session{{UUID: "s1"}}, previewGen: 2}
	v, _ = bUpdate(t, v, previewLoadedMsg{owner: tabBrowse, gen: 2, msgs: []sessions.Message{{Role: "user", Content: "hi"}}})
	if len(v.previewMsgs) != 1 {
		t.Fatalf("preview not populated: %+v", v.previewMsgs)
	}
	if v2, _ := bUpdate(t, v, previewLoadedMsg{owner: tabBrowse, gen: 1}); len(v2.previewMsgs) != 1 {
		t.Fatal("a stale preview generation should be ignored")
	}
	if v3, _ := bUpdate(t, v, previewLoadedMsg{owner: tabSearch, gen: 2}); len(v3.previewMsgs) != 1 {
		t.Fatal("a preview owned by another view should be ignored")
	}
}

// View renders the session list and the preview content.
func TestBrowseViewRendersList(t *testing.T) {
	v := browseView{
		width: 100, height: 30,
		sessions:    []sessions.Session{{UUID: "abcd1234ef", ProjectPath: "/proj", Title: "Auth fix"}},
		previewMsgs: []sessions.Message{{Role: "user", Content: "the auth conversation"}},
	}
	out := v.View()
	if !strings.Contains(out, "Auth fix") {
		t.Fatalf("view should show the session title in the list: %q", out)
	}
	if !strings.Contains(out, "auth conversation") {
		t.Fatalf("view should show preview content: %q", out)
	}
}

// A loaded but empty index shows an empty state instead of a blank pane.
func TestBrowseViewEmptyState(t *testing.T) {
	v := browseView{width: 80, height: 24, loaded: true}
	if !strings.Contains(v.View(), "No sessions") {
		t.Fatalf("loaded-but-empty browse should show an empty state: %q", v.View())
	}
}

// Enter expands a parent to reveal its subagents (lazily loaded) as indented child
// rows, and collapses them again.
func TestBrowseExpandCollapseNestsSubagents(t *testing.T) {
	v := browseView{db: testDB(t)}
	v, _ = v.Update(browseLoadedMsg{sessions: []sessions.Session{{UUID: "P", Title: "parent", SubagentCount: 1}}})
	if len(v.rows()) != 1 {
		t.Fatalf("a collapsed parent is one row, got %d", len(v.rows()))
	}
	// Expand: marks expanded and requests the children.
	v, cmd := v.Update(key(tea.KeyEnter))
	if !v.expanded["P"] {
		t.Fatal("Enter should expand a parent with subagents")
	}
	if cmd == nil {
		t.Fatal("expanding should request the parent's children")
	}
	// Children arrive and nest under the parent as child rows.
	v, _ = v.Update(browseChildrenLoadedMsg{parent: "P", children: []sessions.Session{{UUID: "agent-c", AgentType: "general-purpose", Title: "kid"}}})
	rows := v.rows()
	if len(rows) != 2 || !rows[1].child || rows[1].sess.UUID != "agent-c" {
		t.Fatalf("expanded parent should nest its child: %+v", rows)
	}
	// Navigating down selects the nested child (its transcript previews).
	v, _ = v.Update(key(tea.KeyDown))
	if v.selectedSession() != "agent-c" {
		t.Fatalf("down should select the nested child, got %q", v.selectedSession())
	}
	// Back to the parent and collapse.
	v, _ = v.Update(key(tea.KeyUp))
	v, _ = v.Update(key(tea.KeyEnter))
	if v.expanded["P"] {
		t.Fatal("Enter on an expanded parent should collapse it")
	}
	if len(v.rows()) != 1 {
		t.Fatalf("collapsed again is one row, got %d", len(v.rows()))
	}
}

// When a parent's children arrive after the user has already moved the selection
// below it, the selection shifts to stay on the same session instead of jumping
// onto a freshly inserted child row.
func TestBrowseChildrenArrivalKeepsSelection(t *testing.T) {
	v := browseView{db: testDB(t)}
	v, _ = v.Update(browseLoadedMsg{sessions: []sessions.Session{
		{UUID: "P", Title: "parent", SubagentCount: 1},
		{UUID: "Q", Title: "other"},
	}})
	v, _ = v.Update(key(tea.KeyEnter)) // expand P (selection stays on P, children load async)
	v, _ = v.Update(key(tea.KeyDown))  // move to Q before children arrive
	if v.selectedSession() != "Q" {
		t.Fatalf("precondition: selection should be on Q, got %q", v.selectedSession())
	}
	v, _ = v.Update(browseChildrenLoadedMsg{parent: "P", children: []sessions.Session{{UUID: "agent-c", Title: "kid"}}})
	if v.selectedSession() != "Q" {
		t.Fatalf("selection should stay on Q after a child is inserted above it, got %q", v.selectedSession())
	}
}

// A duplicate child-load response (parent expanded, collapsed, re-expanded before
// the first load returned) must shift the selection only once, not on every reply.
func TestBrowseDuplicateChildrenArrivalShiftsOnce(t *testing.T) {
	v := browseView{db: testDB(t)}
	v, _ = v.Update(browseLoadedMsg{sessions: []sessions.Session{
		{UUID: "P", Title: "parent", SubagentCount: 1},
		{UUID: "Q", Title: "other"},
	}})
	v, _ = v.Update(key(tea.KeyEnter)) // expand P
	v, _ = v.Update(key(tea.KeyDown))  // selected -> Q
	kids := []sessions.Session{{UUID: "agent-c", Title: "kid"}}
	v, _ = v.Update(browseChildrenLoadedMsg{parent: "P", children: kids}) // first arrival shifts
	if v.selectedSession() != "Q" {
		t.Fatalf("after first arrival selection should be Q, got %q", v.selectedSession())
	}
	v, _ = v.Update(browseChildrenLoadedMsg{parent: "P", children: kids}) // duplicate must not shift again
	if v.selectedSession() != "Q" {
		t.Fatalf("a duplicate child-load must not shift selection again, got %q", v.selectedSession())
	}
}

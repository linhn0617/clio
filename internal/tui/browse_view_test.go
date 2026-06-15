package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/linhn0617/clio/internal/sessions"
)

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

// Preview results for the selected session populate the pane; stale ones ignored.
func TestBrowseViewPreviewResults(t *testing.T) {
	v := browseView{sessions: []sessions.Session{{UUID: "s1"}}}
	v, _ = bUpdate(t, v, previewLoadedMsg{sessionUUID: "s1", msgs: []sessions.Message{{Role: "user", Content: "hi"}}})
	if len(v.previewMsgs) != 1 {
		t.Fatalf("preview not populated: %+v", v.previewMsgs)
	}
	v2, _ := bUpdate(t, v, previewLoadedMsg{sessionUUID: "other"})
	if len(v2.previewMsgs) != 1 {
		t.Fatal("stale preview should be ignored")
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

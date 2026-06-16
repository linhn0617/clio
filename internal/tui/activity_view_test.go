package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/sessions"
)

// The drill pane surfaces a drill error; the status line surfaces a list error.
func TestActivityViewSurfacesErrors(t *testing.T) {
	v := activityView{
		width: 80, height: 24, loaded: true,
		entries:  []sessions.ActivityCount{{Value: "/a", Count: 1}},
		drillErr: errors.New("drill boom"),
	}
	if !strings.Contains(v.View(), "drill error") {
		t.Fatalf("the drill pane should surface the drill error: %q", v.View())
	}
	v2 := activityView{width: 80, height: 24, err: errors.New("kind boom")}
	if !strings.Contains(v2.View(), "kind boom") {
		t.Fatalf("the status line should surface the list error: %q", v2.View())
	}
}

func addTarget(t *testing.T, d *db.DB, sess, kind, value string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (0,?,?,?,?)`,
		sess, time.Now().Unix(), kind, value); err != nil {
		t.Fatal(err)
	}
}

func aUpdate(t *testing.T, v activityView, msg tea.Msg) (activityView, tea.Cmd) {
	t.Helper()
	return v.Update(msg)
}

// load aggregates the top values of the current kind (files by default).
func TestActivityViewLoad(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addSession(t, d, "s2", "/p")
	addTarget(t, d, "s1", "file", "/a/b.go")
	addTarget(t, d, "s2", "file", "/a/b.go")
	addTarget(t, d, "s1", "file", "/c.go")
	v := activityView{db: d}
	msg := v.load()()
	al, ok := msg.(activityLoadedMsg)
	if !ok {
		t.Fatalf("load should emit activityLoadedMsg, got %T", msg)
	}
	if al.err != nil || al.kind != "file" || len(al.entries) != 2 {
		t.Fatalf("load result wrong: kind=%s entries=%+v err=%v", al.kind, al.entries, al.err)
	}
	if al.entries[0].Value != "/a/b.go" || al.entries[0].Count != 2 {
		t.Fatalf("top entry should be /a/b.go x2, got %+v", al.entries[0])
	}
}

// Loaded entries for the current kind populate the list and schedule a drill;
// a load for a different kind is ignored.
func TestActivityViewLoadedPopulates(t *testing.T) {
	v := activityView{db: testDB(t)}
	v, cmd := aUpdate(t, v, activityLoadedMsg{kind: "file", entries: []sessions.ActivityCount{{Value: "/a", Count: 2}}})
	if !v.loaded || len(v.entries) != 1 || v.selected != 0 {
		t.Fatalf("entries not populated: %+v", v)
	}
	if cmd == nil {
		t.Fatal("loading entries should schedule a drill of the first")
	}
	v2, _ := aUpdate(t, v, activityLoadedMsg{kind: "command", entries: nil})
	if len(v2.entries) != 1 {
		t.Fatal("a load for a non-current kind should be ignored")
	}
}

// Left/right cycles the kind and reloads.
func TestActivityViewKindSwitch(t *testing.T) {
	v := activityView{db: testDB(t)}
	if v.currentKind() != "file" {
		t.Fatalf("default kind should be file, got %q", v.currentKind())
	}
	v, cmd := aUpdate(t, v, key(tea.KeyRight))
	if v.currentKind() != "command" {
		t.Fatalf("right should advance to command, got %q", v.currentKind())
	}
	if v.loaded {
		t.Fatal("switching kind should reset the loaded flag")
	}
	if cmd == nil {
		t.Fatal("switching kind should reload entries")
	}
	v, _ = aUpdate(t, v, key(tea.KeyLeft))
	if v.currentKind() != "file" {
		t.Fatalf("left should return to file, got %q", v.currentKind())
	}
}

// Switching kind clears the previous kind's rows, drill, and selection so the
// list never shows stale rows under the new kind's label.
func TestActivityViewKindSwitchClearsStaleRows(t *testing.T) {
	v := activityView{
		db:       testDB(t),
		entries:  []sessions.ActivityCount{{Value: "/a"}, {Value: "/b"}},
		selected: 1,
		drill:    []sessions.Session{{UUID: "s1"}},
		loaded:   true,
	}
	v, _ = aUpdate(t, v, key(tea.KeyRight))
	if len(v.entries) != 0 || len(v.drill) != 0 || v.selected != 0 || v.loaded {
		t.Fatalf("switching kind should clear stale rows/drill/selection: %+v", v)
	}
}

// j/k navigate the entry list (clamped) and schedule a drill.
func TestActivityViewNavigation(t *testing.T) {
	v := activityView{db: testDB(t), entries: []sessions.ActivityCount{{Value: "a"}, {Value: "b"}, {Value: "c"}}}
	v, cmd := aUpdate(t, v, runes("j"))
	if v.selected != 1 || cmd == nil {
		t.Fatalf("'j' should move down and drill, selected=%d cmd=%v", v.selected, cmd)
	}
	v, _ = aUpdate(t, v, runes("j"))
	v, _ = aUpdate(t, v, runes("j")) // clamp
	if v.selected != 2 {
		t.Fatalf("selection should clamp at last, got %d", v.selected)
	}
}

// Moving the selection clears the previous entry's drill before loading the new
// one, so the detail pane never shows sessions for a different value.
func TestActivityViewSelectionClearsStaleDrill(t *testing.T) {
	v := activityView{
		entries: []sessions.ActivityCount{{Value: "a"}, {Value: "b"}},
		drill:   []sessions.Session{{UUID: "s1"}},
	}
	v, _ = aUpdate(t, v, runes("j"))
	if len(v.drill) != 0 {
		t.Fatal("changing selection should clear the stale drill before the new load")
	}
}

// The drill command lists the sessions that touched the selected file.
func TestActivityViewDrillCmdFilters(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addTarget(t, d, "s1", "file", "/a/b.go")
	v := activityView{db: d, entries: []sessions.ActivityCount{{Value: "/a/b.go", Count: 1}}}
	msg := v.drillCmd()()
	ad, ok := msg.(activityDrillMsg)
	if !ok {
		t.Fatalf("drillCmd should emit activityDrillMsg, got %T", msg)
	}
	if ad.err != nil || ad.kind != "file" || ad.value != "/a/b.go" || len(ad.sessions) != 1 || ad.sessions[0].UUID != "s1" {
		t.Fatalf("drill result wrong: kind=%s %+v err=%v", ad.kind, ad.sessions, ad.err)
	}
}

// Drill results for the selected entry populate the pane; stale ones are ignored.
func TestActivityViewDrillResults(t *testing.T) {
	v := activityView{entries: []sessions.ActivityCount{{Value: "/a/b.go"}}} // file kind
	v, _ = aUpdate(t, v, activityDrillMsg{kind: "file", value: "/a/b.go", sessions: []sessions.Session{{UUID: "s1"}}})
	if len(v.drill) != 1 {
		t.Fatalf("drill not populated: %+v", v.drill)
	}
	v2, _ := aUpdate(t, v, activityDrillMsg{kind: "file", value: "other"})
	if len(v2.drill) != 1 {
		t.Fatal("stale drill (different value) should be ignored")
	}
}

// A drill result for a different kind (same value) is ignored — guards the case
// where the user switches kind to one whose selected value collides.
func TestActivityViewDrillIgnoresWrongKind(t *testing.T) {
	v := activityView{entries: []sessions.ActivityCount{{Value: "git"}}} // file kind, value "git"
	v, _ = aUpdate(t, v, activityDrillMsg{kind: "command", value: "git", sessions: []sessions.Session{{UUID: "s1"}}})
	if len(v.drill) != 0 {
		t.Fatal("a drill result for a different kind should be ignored")
	}
	v, _ = aUpdate(t, v, activityDrillMsg{kind: "file", value: "git", sessions: []sessions.Session{{UUID: "s1"}}})
	if len(v.drill) != 1 {
		t.Fatal("a drill result for the current kind+value should apply")
	}
}

// View shows the activity entries, the drilled sessions, and the current kind.
func TestActivityViewRenders(t *testing.T) {
	v := activityView{
		width: 100, height: 30, loaded: true,
		entries: []sessions.ActivityCount{{Value: "/a/b.go", Count: 3}},
		drill:   []sessions.Session{{UUID: "abcd1234", Title: "Edit b.go"}},
	}
	out := v.View()
	if !strings.Contains(out, "/a/b.go") {
		t.Fatalf("view should show the activity value: %q", out)
	}
	if !strings.Contains(out, "Edit b.go") {
		t.Fatalf("view should show the drilled session: %q", out)
	}
	if !strings.Contains(out, "files") {
		t.Fatalf("status should name the current kind: %q", out)
	}
}

// A loaded but empty kind shows an empty state.
func TestActivityViewEmptyState(t *testing.T) {
	v := activityView{width: 80, height: 24, loaded: true}
	if !strings.Contains(v.View(), "No activity") {
		t.Fatalf("loaded-but-empty activity should show an empty state: %q", v.View())
	}
}

package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/sessions"
)

// A cancelled context surfaces an error from the shared preview load instead of
// running the query — so quitting the TUI never blocks on in-flight DB work.
func TestPreviewLoadHonorsContext(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addMsg(t, d, "s1", 0, "user", "hi")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	msg := loadSessionPreview(ctx, d, "s1")()
	if msg.(previewLoadedMsg).err == nil {
		t.Fatal("a cancelled context should surface an error from the preview load")
	}
}

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func addSession(t *testing.T, d *db.DB, uuid, project string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, ended_at, turn_count) VALUES (?,?,?,?,1)`,
		uuid, project, uuid+".jsonl", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
}

func addMsg(t *testing.T, d *db.DB, sess string, seq int, role, content string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES (?,?,?,?,?,?)`,
		sess, seq, time.Now().Unix(), role, content, "{}"); err != nil {
		t.Fatal(err)
	}
}

func sUpdate(t *testing.T, v searchView, msg tea.Msg) (searchView, tea.Cmd) {
	t.Helper()
	return v.Update(msg)
}

// Typing updates the query and schedules a debounced search; only the latest
// debounce generation actually fires the query (stale keystrokes are dropped).
func TestSearchViewDebounceGeneration(t *testing.T) {
	v := searchView{db: testDB(t)}
	v, cmd := sUpdate(t, v, runes("au"))
	if v.query != "au" {
		t.Fatalf("query = %q, want au", v.query)
	}
	if cmd == nil {
		t.Fatal("typing should schedule a debounce command")
	}
	// A stale debounce (older generation) must NOT trigger a search.
	if _, c := sUpdate(t, v, searchDebounceMsg{gen: v.gen - 1}); c != nil {
		t.Fatal("stale debounce generation should not fire a search")
	}
	// The current generation fires the search command.
	if _, c := sUpdate(t, v, searchDebounceMsg{gen: v.gen}); c == nil {
		t.Fatal("current debounce generation should fire a search")
	}
}

// Changing the query clears the previous query's results and preview immediately
// and marks the view as searching, so stale hits are never shown or navigable.
func TestSearchViewQueryChangeClearsStaleResults(t *testing.T) {
	v := searchView{
		db:          testDB(t),
		query:       "auth",
		results:     []searchHit{{sessionUUID: "s1"}, {sessionUUID: "s2"}},
		previewMsgs: []sessions.Message{{Content: "x"}},
		selected:    1,
	}
	v, _ = sUpdate(t, v, runes("x"))
	if len(v.results) != 0 || len(v.previewMsgs) != 0 || v.selected != 0 {
		t.Fatalf("changing the query should clear stale results/preview/selection: %+v", v)
	}
	if !v.searching {
		t.Fatal("a pending search should mark the view as searching")
	}
	// Results arriving for the current generation clear the searching flag.
	v, _ = sUpdate(t, v, searchResultsMsg{gen: v.gen, results: []searchHit{{sessionUUID: "s3"}}})
	if v.searching {
		t.Fatal("results arriving should clear the searching flag")
	}
}

// Results for the current generation populate the list; stale results are ignored.
func TestSearchViewResults(t *testing.T) {
	v := searchView{gen: 5}
	res := []searchHit{{sessionUUID: "s1"}, {sessionUUID: "s2"}}
	v, _ = sUpdate(t, v, searchResultsMsg{gen: 5, results: res})
	if len(v.results) != 2 || v.selected != 0 {
		t.Fatalf("results not populated / selection not reset: %+v", v)
	}
	// Stale results (older generation) ignored.
	v2, _ := sUpdate(t, v, searchResultsMsg{gen: 4, results: nil})
	if len(v2.results) != 2 {
		t.Fatal("stale results should be ignored")
	}
}

// Up/Down move the selection, clamped to the result range.
func TestSearchViewSelection(t *testing.T) {
	v := searchView{gen: 1, results: []searchHit{{}, {}, {}}}
	v, _ = sUpdate(t, v, key(tea.KeyDown))
	v, _ = sUpdate(t, v, key(tea.KeyDown))
	if v.selected != 2 {
		t.Fatalf("selected = %d, want 2", v.selected)
	}
	v, _ = sUpdate(t, v, key(tea.KeyDown)) // clamp at end
	if v.selected != 2 {
		t.Fatalf("selection should clamp at the last result, got %d", v.selected)
	}
	for range 5 {
		v, _ = sUpdate(t, v, key(tea.KeyUp))
	}
	if v.selected != 0 {
		t.Fatalf("selection should clamp at 0, got %d", v.selected)
	}
}

// The search command actually queries the index for the current query.
func TestSearchViewRunSearchQueries(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addMsg(t, d, "s1", 0, "user", "the authentication module design")
	v := searchView{db: d, query: "authentication", gen: 1}
	msg := v.runSearch(1)()
	r, ok := msg.(searchResultsMsg)
	if !ok {
		t.Fatalf("runSearch should emit searchResultsMsg, got %T", msg)
	}
	if r.err != nil || len(r.results) != 1 || r.results[0].sessionUUID != "s1" {
		t.Fatalf("runSearch result wrong: %+v err=%v", r.results, r.err)
	}
}

// Moving the selection schedules a preview load for the newly selected session.
func TestSearchViewSelectionSchedulesPreview(t *testing.T) {
	v := searchView{db: testDB(t), results: []searchHit{{sessionUUID: "s1"}, {sessionUUID: "s2"}}}
	_, cmd := sUpdate(t, v, key(tea.KeyDown))
	if cmd == nil {
		t.Fatal("moving the selection should schedule a preview load")
	}
	// With no results there is nothing to preview.
	if _, c := sUpdate(t, searchView{db: testDB(t)}, key(tea.KeyDown)); c != nil {
		t.Fatal("no results should not schedule a preview load")
	}
}

// The preview load command reads the selected session's messages.
func TestSearchViewLoadPreviewQueries(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addMsg(t, d, "s1", 0, "user", "hello world")
	addMsg(t, d, "s1", 1, "assistant", "hi there")
	v := searchView{db: d, results: []searchHit{{sessionUUID: "s1"}}}
	msg := v.loadPreview()()
	pm, ok := msg.(previewLoadedMsg)
	if !ok {
		t.Fatalf("loadPreview should emit previewLoadedMsg, got %T", msg)
	}
	if pm.err != nil || pm.sessionUUID != "s1" || len(pm.msgs) != 2 {
		t.Fatalf("preview wrong: %+v err=%v", pm.msgs, pm.err)
	}
}

// Preview results for the selected session populate the pane; stale results for
// a different session are ignored.
func TestSearchViewPreviewResults(t *testing.T) {
	v := searchView{results: []searchHit{{sessionUUID: "s1"}}}
	v, _ = sUpdate(t, v, previewLoadedMsg{sessionUUID: "s1", msgs: []sessions.Message{{Role: "user", Content: "hi"}}})
	if len(v.previewMsgs) != 1 {
		t.Fatalf("preview not populated: %+v", v.previewMsgs)
	}
	v2, _ := sUpdate(t, v, previewLoadedMsg{sessionUUID: "other", msgs: nil})
	if len(v2.previewMsgs) != 1 {
		t.Fatal("stale preview (different selected session) should be ignored")
	}
}

// firstPreviewMatch finds the first message whose content matches a query term,
// case-insensitively; -1 when none match.
func TestFirstPreviewMatch(t *testing.T) {
	msgs := []sessions.Message{
		{Role: "user", Content: "let's discuss the schema"},
		{Role: "assistant", Content: "the authentication module handles login"},
	}
	if got := firstPreviewMatch(msgs, "authentication"); got != 1 {
		t.Fatalf("firstPreviewMatch = %d, want 1", got)
	}
	if got := firstPreviewMatch(msgs, "LOGIN"); got != 1 {
		t.Fatalf("case-insensitive match = %d, want 1", got)
	}
	if got := firstPreviewMatch(msgs, "nonexistent"); got != -1 {
		t.Fatalf("no match should be -1, got %d", got)
	}
}

// View renders both panes of the master-detail layout: the results list and the
// session preview.
func TestSearchViewRendersMasterDetail(t *testing.T) {
	v := searchView{
		width: 100, height: 30, query: "auth",
		results:     []searchHit{{sessionUUID: "s1", project: "/proj", role: "user", snippet: "the [auth] module"}},
		previewMsgs: []sessions.Message{{Role: "assistant", Content: "the auth module design notes"}},
	}
	out := v.View()
	if !strings.Contains(out, "module") {
		t.Fatalf("view should show the result snippet (left pane): %q", out)
	}
	if !strings.Contains(out, "design notes") {
		t.Fatalf("view should show the preview content (right pane): %q", out)
	}
}

// The matched preview message is marked so the hit stands out in context.
func TestSearchViewPreviewMarksMatch(t *testing.T) {
	v := searchView{
		width: 100, height: 30, query: "login",
		previewMsgs: []sessions.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "the login flow"},
		},
	}
	if out := v.View(); !strings.Contains(out, previewMatchMarker) {
		t.Fatalf("matched preview line should carry the match marker: %q", out)
	}
}

// The status line surfaces errors instead of crashing the view.
func TestSearchViewStatusShowsError(t *testing.T) {
	v := searchView{width: 80, height: 24, err: errors.New("boom")}
	if !strings.Contains(v.View(), "boom") {
		t.Fatalf("status line should surface the error: %q", v.View())
	}
}

package tui

import (
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/linhn0617/clio/internal/db"
)

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

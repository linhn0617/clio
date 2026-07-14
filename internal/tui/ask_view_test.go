package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/linhn0617/clio/internal/ask"
)

func qUpdate(t *testing.T, v askView, msg tea.Msg) (askView, tea.Cmd) {
	t.Helper()
	return v.Update(msg)
}

// Typing builds the question; Enter submits it (bumping the generation), a blank
// question does not.
func TestAskViewTypingAndSubmit(t *testing.T) {
	v := askView{db: testDB(t)}
	v, _ = qUpdate(t, v, runes("auth"))
	if v.query != "auth" {
		t.Fatalf("query = %q, want auth", v.query)
	}
	v, cmd := qUpdate(t, v, key(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("Enter on a non-empty question should run ask")
	}
	if _, c := qUpdate(t, askView{db: testDB(t)}, key(tea.KeyEnter)); c != nil {
		t.Fatal("Enter on a blank question should not run ask")
	}
}

// runAsk builds the evidence bundle for the current question.
func TestAskViewRunAskQueries(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addMsg(t, d, "s1", 0, "user", "the authentication module design")
	v := askView{db: d, query: "authentication", gen: 1}
	msg := v.runAsk(1)()
	am, ok := msg.(askAnswerMsg)
	if !ok {
		t.Fatalf("runAsk should emit askAnswerMsg, got %T", msg)
	}
	if am.err != nil || len(am.groups) != 1 || am.groups[0].SessionUUID != "s1" {
		t.Fatalf("ask result wrong: %+v err=%v", am.groups, am.err)
	}
}

// runAsk scopes the query to the view's source filter, so `clio tui --source
// codex` actually asks over codex history instead of silently falling back to
// the claude-code default.
func TestAskViewRunAskUsesSource(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "cc1", "/p") // defaults to claude-code (NULL source column)
	addMsg(t, d, "cc1", 0, "user", "the authentication module design")
	addSessionWithSource(t, d, "cx1", "/p", "codex")
	addMsg(t, d, "cx1", 0, "user", "the authentication module design")

	v := askView{db: d, query: "authentication", gen: 1, source: "codex"}
	msg := v.runAsk(1)()
	am, ok := msg.(askAnswerMsg)
	if !ok {
		t.Fatalf("runAsk should emit askAnswerMsg, got %T", msg)
	}
	if am.err != nil || len(am.groups) != 1 || am.groups[0].SessionUUID != "cx1" {
		t.Fatalf("askView with source=codex should only match codex sessions: %+v err=%v", am.groups, am.err)
	}
}

// Editing the question after submitting bumps the generation so an in-flight
// answer for the previous question is dropped (no evidence/header mismatch).
func TestAskViewEditingAfterSubmitDropsStaleAnswer(t *testing.T) {
	v := askView{db: testDB(t), query: "auth", gen: 1} // submitted at gen 1
	v, _ = qUpdate(t, v, runes("z"))                   // keep editing before the answer returns
	if v.gen == 1 {
		t.Fatal("editing after submit should bump gen so the in-flight answer is dropped")
	}
	v, _ = qUpdate(t, v, askAnswerMsg{gen: 1, groups: []ask.EvidenceGroup{{SessionUUID: "s1"}}})
	if len(v.groups) != 0 {
		t.Fatalf("an answer for the pre-edit generation should be ignored, got %d groups", len(v.groups))
	}
}

// Answers for the current generation populate the groups; stale ones are ignored.
func TestAskViewAnswerResults(t *testing.T) {
	v := askView{gen: 3}
	v, _ = qUpdate(t, v, askAnswerMsg{gen: 3, groups: []ask.EvidenceGroup{{SessionUUID: "s1"}, {SessionUUID: "s2"}}})
	if len(v.groups) != 2 || v.selected != 0 || v.loading {
		t.Fatalf("groups not populated: %+v", v)
	}
	v2, _ := qUpdate(t, v, askAnswerMsg{gen: 2, groups: nil})
	if len(v2.groups) != 2 {
		t.Fatal("stale answer (older generation) should be ignored")
	}
}

// Up/Down move the selection among groups, clamped.
func TestAskViewSelection(t *testing.T) {
	v := askView{groups: []ask.EvidenceGroup{{}, {}, {}}}
	v, _ = qUpdate(t, v, key(tea.KeyDown))
	v, _ = qUpdate(t, v, key(tea.KeyDown))
	v, _ = qUpdate(t, v, key(tea.KeyDown)) // clamp
	if v.selected != 2 {
		t.Fatalf("selection should clamp at last group, got %d", v.selected)
	}
	for range 5 {
		v, _ = qUpdate(t, v, key(tea.KeyUp))
	}
	if v.selected != 0 {
		t.Fatalf("selection should clamp at 0, got %d", v.selected)
	}
}

// View shows the group list, the selected group's excerpts, and marks the hit.
func TestAskViewRendersEvidence(t *testing.T) {
	v := askView{
		width: 100, height: 30, answered: "auth", query: "auth",
		groups: []ask.EvidenceGroup{{
			SessionUUID: "abcd1234ef", Title: "Auth design",
			Excerpts: []ask.Excerpt{
				{Role: "user", Text: "how does auth work", IsHit: true},
				{Role: "assistant", Text: "it uses tokens"},
			},
		}},
	}
	out := v.View()
	if !strings.Contains(out, "Auth design") {
		t.Fatalf("view should list the group title: %q", out)
	}
	if !strings.Contains(out, "how does auth work") {
		t.Fatalf("view should show the selected group's excerpts: %q", out)
	}
	// One marker for the selected group row, one for the hit excerpt: dropping the
	// IsHit marking would leave only the list marker.
	if n := strings.Count(out, previewMatchMarker); n < 2 {
		t.Fatalf("the matched excerpt should be marked (markers=%d): %q", n, out)
	}
}

// The typed question is visible so the user can see what they're asking.
func TestAskViewShowsQuery(t *testing.T) {
	v := askView{width: 80, height: 24, query: "how does login work"}
	if !strings.Contains(v.View(), "how does login work") {
		t.Fatalf("view should show the typed question: %q", v.View())
	}
}

// Asking with no matching evidence shows an empty state.
func TestAskViewEmptyState(t *testing.T) {
	v := askView{width: 80, height: 24, answered: "nothing", query: "nothing"}
	if !strings.Contains(v.View(), "No evidence") {
		t.Fatalf("asked-but-empty should show an empty state: %q", v.View())
	}
}

// Submitting a new question clears the previous answer immediately and shows a
// loading state, so old evidence isn't shown under the new question.
func TestAskViewNewQuestionClearsStaleAnswer(t *testing.T) {
	v := askView{
		db: testDB(t), query: "auth", answered: "auth",
		groups: []ask.EvidenceGroup{{SessionUUID: "s1"}},
	}
	v, _ = qUpdate(t, v, runes("2")) // query -> "auth2"
	v, cmd := qUpdate(t, v, key(tea.KeyEnter))
	if len(v.groups) != 0 || !v.loading {
		t.Fatalf("submitting a new question should clear the old answer and show loading: groups=%d loading=%v", len(v.groups), v.loading)
	}
	if cmd == nil {
		t.Fatal("submitting should dispatch a new ask")
	}
}

// Editing the question (without resubmitting) hides the stale answer, so the
// panes never show evidence for a question the user has moved past.
func TestAskViewEditedQueryHidesStaleAnswer(t *testing.T) {
	v := askView{
		width: 80, height: 24, query: "auth", answered: "auth",
		groups: []ask.EvidenceGroup{{
			SessionUUID: "abcd", Title: "Auth design",
			Excerpts: []ask.Excerpt{{Role: "user", Text: "hi"}},
		}},
	}
	if !strings.Contains(v.View(), "Auth design") {
		t.Fatalf("should show the answer while the query matches: %q", v.View())
	}
	v, _ = qUpdate(t, v, runes("x")) // query -> "authx", diverges from answered
	if strings.Contains(v.View(), "Auth design") {
		t.Fatalf("after editing the query, the stale answer should be hidden: %q", v.View())
	}
}

// A narrow terminal must not let the question header row overflow the
// terminal width — a long question should be clamped like every other row.
func TestAskViewHeaderNarrowNoOverflow(t *testing.T) {
	v := askView{width: 20, height: 10, query: strings.Repeat("x", 50)}
	line := strings.SplitN(v.View(), "\n", 2)[0]
	if w := runewidth.StringWidth(line); w > 20 {
		t.Fatalf("ask header exceeds terminal width 20 (got %d): %q", w, line)
	}
}

// Even a 1-column terminal must not overflow: the "…" ellipsis is itself
// width 2, so the header needs an empty tail there (like masterDetail).
func TestAskViewHeaderWidth1NoOverflow(t *testing.T) {
	v := askView{width: 1, height: 10, query: strings.Repeat("x", 50)}
	line := strings.SplitN(v.View(), "\n", 2)[0]
	if w := runewidth.StringWidth(line); w > 1 {
		t.Fatalf("ask header exceeds terminal width 1 (got %d): %q", w, line)
	}
}

package sessions

import (
	"context"
	"slices"
	"testing"
)

func seqsOf(ms []Message) []int {
	out := make([]int, len(ms))
	for i, m := range ms {
		out[i] = m.Seq
	}
	return out
}

// A window is taken in conversational (user/assistant) turn space: a run of
// tool_use/tool_result events between a question and its answer must not consume
// the window or appear in it.
func TestGetWindowSkipsToolOutput(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 6)
	addMsg(t, d, "s1", 0, "user", "earlier question")
	addMsg(t, d, "s1", 1, "tool_use", "noise")
	addMsg(t, d, "s1", 2, "tool_result", "noise")
	addMsg(t, d, "s1", 3, "assistant", "earlier answer")
	addMsg(t, d, "s1", 4, "user", "HIT question")
	addMsg(t, d, "s1", 5, "assistant", "HIT answer")

	win, err := GetWindow(context.Background(), d, "s1", 4, 1, 1, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := seqsOf(win); !slices.Equal(got, []int{3, 4, 5}) {
		t.Fatalf("window seqs = %v, want [3 4 5]", got)
	}
	for _, m := range win {
		if m.Role != "user" && m.Role != "assistant" {
			t.Fatalf("window contains a non-dialogue role %q", m.Role)
		}
	}
}

// The window clamps at the start of the session rather than under-running.
func TestGetWindowClampsAtStart(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", 3)
	addMsg(t, d, "s1", 0, "user", "first")
	addMsg(t, d, "s1", 1, "assistant", "second")
	addMsg(t, d, "s1", 2, "user", "third")

	win, err := GetWindow(context.Background(), d, "s1", 0, 2, 1, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := seqsOf(win); !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("window seqs = %v, want [0 1]", got)
	}
}

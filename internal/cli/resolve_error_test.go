package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/sessions"
)

// `clio show` renders an ambiguous id with `clio list`'s time and title format.
func TestResolveErrorUsesListFormatting(t *testing.T) {
	ts := time.Date(2026, 10, 1, 9, 30, 0, 0, time.Local).Unix()
	amb := &sessions.AmbiguousError{Prefix: "ab", Candidates: []sessions.Candidate{
		{Session: sessions.Session{UUID: "abc11111-x", StartedAt: ts, Title: "first line\nsecond"}, ID: "abc11111"},
	}}
	err := resolveError(amb)
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "\n  abc11111  "+formatTS(ts)+"  "+oneLine("first line\nsecond", 60)) {
		t.Fatalf("not rendered with list formatting:\n%s", msg)
	}
	if other := errors.New("x"); resolveError(other) != other {
		t.Fatal("non-ambiguity errors must pass through unchanged")
	}
}

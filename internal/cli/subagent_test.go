package cli

import (
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/sessions"
)

func TestFormatSessionLineAnnotatesSubagentCount(t *testing.T) {
	none := formatSessionLine(sessions.Session{UUID: "p1", Title: "parent"})
	if strings.Contains(none, "subagent") {
		t.Fatalf("no count → no annotation: %q", none)
	}
	one := formatSessionLine(sessions.Session{UUID: "p1", Title: "parent", SubagentCount: 1})
	if !strings.Contains(one, "(+1 subagent)") {
		t.Fatalf("count 1 → singular: %q", one)
	}
	many := formatSessionLine(sessions.Session{UUID: "p1", Title: "parent", SubagentCount: 3})
	if !strings.Contains(many, "(+3 subagents)") {
		t.Fatalf("count 3 → plural: %q", many)
	}
}

func TestSubagentNote(t *testing.T) {
	if note := subagentNote(sessions.Session{UUID: "agent-x"}); note != "" {
		t.Fatalf("non-subagent → empty note, got %q", note)
	}
	note := subagentNote(sessions.Session{UUID: "agent-x", ParentSession: "parent-1234567890", AgentType: "general-purpose"})
	if !strings.Contains(note, "subagent") || !strings.Contains(note, "general-purpose") || !strings.Contains(note, shortID("parent-1234567890")) {
		t.Fatalf("subagent note missing parts: %q", note)
	}
	// Empty type degrades gracefully (no empty parens).
	bare := subagentNote(sessions.Session{UUID: "agent-x", ParentSession: "parent-1234567890"})
	if !strings.Contains(bare, "subagent") || strings.Contains(bare, "()") {
		t.Fatalf("empty-type note should not show empty parens: %q", bare)
	}
}

func TestFormatSubagentsSection(t *testing.T) {
	if s := formatSubagentsSection(nil); s != "" {
		t.Fatalf("no children → empty section, got %q", s)
	}
	children := []sessions.Session{
		{UUID: "agent-aaaa1111", AgentType: "general-purpose", Title: "explore the codebase"},
		{UUID: "agent-bbbb2222", AgentType: "Explore", Title: "find tests"},
	}
	sec := formatSubagentsSection(children)
	for _, want := range []string{"Subagents", "general-purpose", "explore the codebase", shortID("agent-bbbb2222"), "Explore"} {
		if !strings.Contains(sec, want) {
			t.Fatalf("section missing %q: %q", want, sec)
		}
	}
}

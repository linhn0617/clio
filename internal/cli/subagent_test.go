package cli

import (
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/search"
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

func TestFormatSearchResultLabelsSubagent(t *testing.T) {
	normal := formatSearchResult(search.Result{SessionUUID: "s1", Role: "user", Snippet: "hello"})
	if strings.Contains(normal, "↳") {
		t.Fatalf("a normal hit should not be labeled: %q", normal)
	}
	sub := formatSearchResult(search.Result{SessionUUID: "agent-c", ParentSession: "P", AgentType: "Explore", Role: "assistant", Snippet: "found"})
	if !strings.Contains(sub, "↳") || !strings.Contains(sub, "Explore") {
		t.Fatalf("a subagent hit should be labeled with ↳ and its type: %q", sub)
	}
}

func TestSearchAndListLinesStripTerminalEscapes(t *testing.T) {
	s := sessions.Session{UUID: "abcdef123", Title: "t\x1b[2Jitle", ProjectPath: "/p/pro\x1b]52;c;QQ==\x07j", TurnCount: 1}
	r := search.Result{SessionUUID: "abcdef123", ProjectPath: "/p/pr\x1b[2Joj", Role: "user", ParentSession: "x", AgentType: "a\x1b[2Jt",
		Snippet: "sn\x1b]0;pwn\x07ip"}
	for name, out := range map[string]string{
		"list":   formatSessionLine(s),
		"search": formatSearchResult(r),
		"usage":  formatUsageSessionRow(sessions.UsageSessionRow{SessionUUID: "abcdef123", ProjectPath: "/p/pr\x1b[2Joj", Title: "t\x1b[2J", AgentType: "a\x1b[2J"}),
		"group":  formatUsageGroupRow("project", sessions.UsageGroupRow{Key: "/p/pr\x1b[2Joj"}),
	} {
		if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x07) {
			t.Errorf("%s line carries a control byte: %q", name, out)
		}
	}
}

func TestFormatActivityRowStripsTerminalEscapes(t *testing.T) {
	out := formatActivityRow(sessions.ActivityCount{Value: "ls\x1b]52;c;QQ==\x07 -l\x1b[2J", Count: 2})
	if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x07) || !strings.Contains(out, "ls -l") {
		t.Fatalf("activity row not clean: %q", out)
	}
}

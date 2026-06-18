package cli

import (
	"fmt"
	"strings"

	"github.com/linhn0617/clio/internal/sessions"
)

// formatSessionLine renders one `clio list` row, annotating a parent that spawned
// subagents with its child count.
func formatSessionLine(s sessions.Session) string {
	line := fmt.Sprintf("%s  %s  %3d turns  %s  %s",
		shortID(s.UUID), formatTS(s.EndedAt), s.TurnCount, trimProject(s.ProjectPath), oneLine(s.Title, 60))
	if s.SubagentCount > 0 {
		noun := "subagents"
		if s.SubagentCount == 1 {
			noun = "subagent"
		}
		line += fmt.Sprintf("  (+%d %s)", s.SubagentCount, noun)
	}
	return line
}

// subagentNote returns a one-line banner identifying a subagent transcript by its
// type and parent session; empty for a normal (top-level) session.
func subagentNote(s sessions.Session) string {
	if s.ParentSession == "" {
		return ""
	}
	if s.AgentType != "" {
		return fmt.Sprintf("↳ subagent (%s) of %s", s.AgentType, shortID(s.ParentSession))
	}
	return fmt.Sprintf("↳ subagent of %s", shortID(s.ParentSession))
}

// formatSubagentsSection lists a parent's subagents (id · type · title) as a
// markdown section; empty when there are none.
func formatSubagentsSection(children []sessions.Session) string {
	if len(children) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Subagents\n\n")
	for _, c := range children {
		typ := c.AgentType
		if typ == "" {
			typ = "subagent"
		}
		fmt.Fprintf(&b, "- %s · %s · %s\n", shortID(c.UUID), typ, oneLine(c.Title, 60))
	}
	return b.String()
}

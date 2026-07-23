package tui

import (
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/sessions"
)

func TestBrowseRenderListShowsTokensAndStale(t *testing.T) {
	parent := sessions.Session{UUID: "parent-uuid-123", Title: "top level", SubagentCount: 1}
	child := sessions.Session{UUID: "child-uuid-456", Title: "child work", AgentType: "general-purpose", ParentSession: parent.UUID}
	v := browseView{
		width: 120, height: 20, loaded: true,
		sessions:   []sessions.Session{parent},
		expanded:   map[string]bool{parent.UUID: true},
		kids:       map[string][]sessions.Session{parent.UUID: {child}},
		tokens:     map[string]int64{parent.UUID: 1_200_000, child.UUID: 34_000},
		staleUsage: map[string]bool{child.UUID: true},
	}
	out := v.renderList(120, 20)
	if !strings.Contains(out, "[1.2M tok]") {
		t.Fatalf("top-level token column missing:\n%s", out)
	}
	if !strings.Contains(out, "[34k tok, stale]") {
		t.Fatalf("child token+stale column missing:\n%s", out)
	}
}

func TestActivityRenderDrillShowsTokensAndStale(t *testing.T) {
	s1 := sessions.Session{UUID: "drill-uuid-789", Title: "drilled session"}
	v := activityView{
		drill:       []sessions.Session{s1},
		drillTokens: map[string]int64{s1.UUID: 1500},
		drillStale:  map[string]bool{s1.UUID: true},
	}
	out := v.renderDrill()
	if !strings.Contains(out, "[1.5k tok, stale]") {
		t.Fatalf("drill token+stale suffix missing:\n%s", out)
	}
}

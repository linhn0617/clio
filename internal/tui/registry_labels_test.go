package tui

import (
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/registry"
	"github.com/linhn0617/clio/internal/sessions"
)

// TestBrowseViewLabelsFromRegistryGoldenTwoSourceSeed pins Browse's row
// labeling to the pre-registry hardcoded values: codex rows get "[codex] ",
// claude-code rows get no label (design.md D9 golden-test gate).
func TestBrowseViewLabelsFromRegistryGoldenTwoSourceSeed(t *testing.T) {
	v := browseView{
		width: 100, height: 30,
		sessions: []sessions.Session{
			{UUID: "a1", Title: "cc session", Source: "claude-code"},
			{UUID: "a2", Title: "cx session", Source: "codex"},
		},
	}
	out := v.View()
	if !strings.Contains(out, "[codex] cx session") {
		t.Fatalf("expected the codex row labeled [codex], got %q", out)
	}
	if strings.Contains(out, "] cc session") {
		t.Fatalf("claude-code row should not be labeled, got %q", out)
	}
}

// TestBrowseViewLabelTracksFakeSeedEntry proves the label is derived from the
// registry, not a hardcoded "codex" literal (tasks.md §3.1): a fake entry's
// label appears without editing browse_view.go.
func TestBrowseViewLabelTracksFakeSeedEntry(t *testing.T) {
	orig := registry.Seed
	t.Cleanup(func() { registry.Seed = orig })
	registry.Seed = append(append([]registry.Entry{}, orig...), registry.Entry{
		Name:  "fake-tool",
		Label: "[fake-tool]",
	})

	v := browseView{
		width: 100, height: 30,
		sessions: []sessions.Session{
			{UUID: "a1", Title: "ft session", Source: "fake-tool"},
		},
	}
	out := v.View()
	if !strings.Contains(out, "[fake-tool] ft session") {
		t.Fatalf("expected the fake-tool row labeled [fake-tool], got %q", out)
	}
}

// TestSearchViewLabelsFromRegistryGoldenTwoSourceSeed is search_view's
// equivalent of the Browse golden test above.
func TestSearchViewLabelsFromRegistryGoldenTwoSourceSeed(t *testing.T) {
	v := searchView{width: 100, height: 30, query: "x"}
	v.results = []searchHit{
		{sessionUUID: "a1", snippet: "cc hit", source: "claude-code"},
		{sessionUUID: "a2", snippet: "cx hit", source: "codex"},
	}
	out := v.renderList(100, 10)
	if !strings.Contains(out, "[codex] cx hit") {
		t.Fatalf("expected the codex hit labeled [codex], got %q", out)
	}
	if strings.Contains(out, "] cc hit") {
		t.Fatalf("claude-code hit should not be labeled, got %q", out)
	}
}

// TestSearchViewLabelTracksFakeSeedEntry is search_view's equivalent of the
// Browse fake-seed-entry proof above.
func TestSearchViewLabelTracksFakeSeedEntry(t *testing.T) {
	orig := registry.Seed
	t.Cleanup(func() { registry.Seed = orig })
	registry.Seed = append(append([]registry.Entry{}, orig...), registry.Entry{
		Name:  "fake-tool",
		Label: "[fake-tool]",
	})

	v := searchView{width: 100, height: 30, query: "x"}
	v.results = []searchHit{{sessionUUID: "a1", snippet: "ft hit", source: "fake-tool"}}
	out := v.renderList(100, 10)
	if !strings.Contains(out, "[fake-tool] ft hit") {
		t.Fatalf("expected the fake-tool hit labeled [fake-tool], got %q", out)
	}
}

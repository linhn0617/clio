package mcp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/registry"
)

// sourceTools are the five read tools whose source enum/default must be
// registry-derived (design.md D5/D6).
var sourceTools = []string{"search", "ask", "list_sessions", "activity_summary", "read_session"}

// sourceProperty extracts the "source" property's schema map from a tool's
// input schema, failing the test if it's missing or the wrong shape.
func sourceProperty(t *testing.T, toolName string, props map[string]any) map[string]any {
	t.Helper()
	raw, ok := props["source"]
	if !ok {
		t.Fatalf("%s: no source property", toolName)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s: source property is %T, want map[string]any", toolName, raw)
	}
	return m
}

// TestFiveToolsSourceEnumGoldenThreeSourceSeed pins every read tool's source
// enum + default to the pre-registry hardcoded values (openspec change
// 2026-07-14-generalize-source-adapter-spi golden-test gate).
func TestFiveToolsSourceEnumGoldenThreeSourceSeed(t *testing.T) {
	d := testDB(t)
	s := NewServer(d, "test", nil)
	tools := s.ListTools()

	wantEnum := []string{"claude-code", "codex", "gemini", "all"}
	// wantDescription pins each tool's "source" parameter description to its
	// pre-registry hardcoded literal (codex review P1 finding #1: these must
	// keep reading identically after switching to registry-derived
	// generation).
	wantDescription := map[string]string{
		"search":           "Which tool's history: claude-code (default), codex, gemini, or all",
		"ask":              "Which tool's history: claude-code (default), codex, gemini, or all",
		"list_sessions":    "Which tool's history: claude-code (default), codex, gemini, or all",
		"activity_summary": "Which tool's history: claude-code (default), codex, gemini, or all",
		"read_session":     "Source filter for prefix resolution: claude-code (default), codex, gemini, or all",
	}
	for _, name := range sourceTools {
		st, ok := tools[name]
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		src := sourceProperty(t, name, st.Tool.InputSchema.Properties)
		if got, ok := src["enum"].([]string); !ok || !reflect.DeepEqual(got, wantEnum) {
			t.Errorf("%s: source enum = %v, want %v", name, src["enum"], wantEnum)
		}
		if got, ok := src["default"].(string); !ok || got != "claude-code" {
			t.Errorf("%s: source default = %v, want %q", name, src["default"], "claude-code")
		}
		if got, ok := src["description"].(string); !ok || got != wantDescription[name] {
			t.Errorf("%s: source description = %v, want %q", name, src["description"], wantDescription[name])
		}
	}
}

// TestFiveToolsSourceEnumTracksFakeSeedEntry proves the enum is derived from
// the registry, not a hardcoded literal (tasks.md §3.1): extending
// registry.Seed makes the fake name appear in every tool's enum without
// editing server.go.
func TestFiveToolsSourceEnumTracksFakeSeedEntry(t *testing.T) {
	orig := registry.Seed
	t.Cleanup(func() { registry.Seed = orig })
	registry.Seed = append(append([]registry.Entry{}, orig...), registry.Entry{
		Name:    "fake-tool",
		RootDir: func() (string, error) { return "/nonexistent/fake-tool", nil },
	})

	d := testDB(t)
	s := NewServer(d, "test", nil)
	tools := s.ListTools()

	wantEnum := []string{"claude-code", "codex", "gemini", "fake-tool", "all"}
	for _, name := range sourceTools {
		st := tools[name]
		src := sourceProperty(t, name, st.Tool.InputSchema.Properties)
		if got, ok := src["enum"].([]string); !ok || !reflect.DeepEqual(got, wantEnum) {
			t.Errorf("%s: source enum with fake entry = %v, want %v", name, src["enum"], wantEnum)
		}
		// The description prose must also be generated from the registry, not a
		// hardcoded per-tool sentence (codex review P1 finding #1): the fake
		// source must appear in it too.
		desc, ok := src["description"].(string)
		if !ok || !strings.Contains(desc, "fake-tool") {
			t.Errorf("%s: source description with fake entry = %v, want it to contain %q", name, src["description"], "fake-tool")
		}
	}
}

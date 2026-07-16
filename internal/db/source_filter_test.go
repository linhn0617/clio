package db

import (
	"reflect"
	"testing"

	"github.com/linhn0617/clio/internal/registry"
)

// TestSourceFilterGoldenTwoSourceSeed pins SourceFilter's behavior to the
// pre-registry hardcoded values (design.md D8 golden-test gate).
func TestSourceFilterGoldenTwoSourceSeed(t *testing.T) {
	cases := []struct {
		source   string
		wantSQL  string
		wantArgs []any
	}{
		{"all", "", nil},
		{"", " AND (s IS NULL OR s = ?)", []any{"claude-code"}},
		{"claude-code", " AND (s IS NULL OR s = ?)", []any{"claude-code"}},
		{"codex", " AND s = ?", []any{"codex"}},
	}
	for _, c := range cases {
		sql, args := SourceFilter("s", c.source)
		if sql != c.wantSQL || !reflect.DeepEqual(args, c.wantArgs) {
			t.Errorf("SourceFilter(%q) = (%q, %v), want (%q, %v)", c.source, sql, args, c.wantSQL, c.wantArgs)
		}
	}
}

// TestSourceFilterDefaultTracksRegistry proves the "" default-source
// fallback is derived from registry.DefaultSource(), not the hardcoded
// model.SourceClaudeCode literal (tasks.md §3.1): swapping the registry's
// default source changes what an empty source resolves to.
func TestSourceFilterDefaultTracksRegistry(t *testing.T) {
	orig := registry.Seed
	t.Cleanup(func() { registry.Seed = orig })
	registry.Seed = []registry.Entry{
		{Name: "custom-default", RootDir: func() (string, error) { return "", nil }},
		{Name: "codex", RootDir: func() (string, error) { return "", nil }},
	}

	sql, args := SourceFilter("s", "")
	wantSQL := " AND (s IS NULL OR s = ?)"
	wantArgs := []any{"custom-default"}
	if sql != wantSQL || !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("SourceFilter(\"\") with fake default = (%q, %v), want (%q, %v)", sql, args, wantSQL, wantArgs)
	}

	// The registry's default name itself (not just "") must also hit the
	// NULL-coalescing branch, mirroring today's claude-code behavior.
	sql2, args2 := SourceFilter("s", "custom-default")
	if sql2 != wantSQL || !reflect.DeepEqual(args2, wantArgs) {
		t.Errorf("SourceFilter(custom-default) with fake default = (%q, %v), want (%q, %v)", sql2, args2, wantSQL, wantArgs)
	}
}

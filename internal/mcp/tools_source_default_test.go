package mcp

import (
	"testing"

	"github.com/linhn0617/clio/internal/registry"
)

// withFakeDefaultSource swaps the registry so its default source is
// "custom-default" (not "claude-code"), restoring the original Seed on
// cleanup. Used to prove each handler's "source" fallback is derived from
// registry.DefaultSource() rather than a hardcoded "claude-code" literal
// (design.md D6).
func withFakeDefaultSource(t *testing.T) {
	t.Helper()
	orig := registry.Seed
	t.Cleanup(func() { registry.Seed = orig })
	registry.Seed = []registry.Entry{
		{Name: "custom-default", RootDir: func() (string, error) { return "", nil }},
		{Name: "codex", RootDir: func() (string, error) { return "", nil }},
	}
}

func TestHandleListSessionsDefaultSourceComesFromRegistry(t *testing.T) {
	withFakeDefaultSource(t)
	d := testDB(t)
	addSessionWithSource(t, d, "cd1", "/p", "custom-default")
	addSessionWithSource(t, d, "cx1", "/p", "codex")

	m := resultJSON(t, call(t, handleListSessions(d, nil), map[string]any{}))
	rows, _ := m["sessions"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 session filtered to the registry default, got %d: %v", len(rows), rows)
	}
	row, _ := rows[0].(map[string]any)
	if row["uuid"] != "cd1" {
		t.Errorf("expected the custom-default session, got %v", row)
	}
}

func TestHandleSearchDefaultSourceComesFromRegistry(t *testing.T) {
	withFakeDefaultSource(t)
	d := testDB(t)
	addSessionWithSource(t, d, "cd1", "/p", "custom-default")
	addMsg(t, d, "cd1", 1, "user", "widget gadget")
	addSessionWithSource(t, d, "cx1", "/p", "codex")
	addMsg(t, d, "cx1", 1, "user", "widget gadget")

	m := resultJSON(t, call(t, handleSearch(d, nil), map[string]any{"query": "widget"}))
	rows, _ := m["results"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 result filtered to the registry default, got %d: %v", len(rows), rows)
	}
	row, _ := rows[0].(map[string]any)
	if row["session_uuid"] != "cd1" {
		t.Errorf("expected the custom-default session's hit, got %v", row)
	}
}

func TestHandleActivitySummaryDefaultSourceComesFromRegistry(t *testing.T) {
	withFakeDefaultSource(t)
	d := testDB(t)
	addSessionWithSource(t, d, "cd1", "/p", "custom-default")
	addSessionWithSource(t, d, "cx1", "/p", "codex")

	m := resultJSON(t, call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "project"}))
	buckets, _ := m["buckets"].([]any)
	if len(buckets) != 1 {
		t.Fatalf("expected 1 bucket filtered to the registry default, got %d: %v", len(buckets), buckets)
	}
}

func TestHandleAskDefaultSourceComesFromRegistry(t *testing.T) {
	withFakeDefaultSource(t)
	d := testDB(t)
	addSessionWithSource(t, d, "cd1", "/p", "custom-default")
	addMsg(t, d, "cd1", 1, "user", "authentication bug repro")
	addSessionWithSource(t, d, "cx1", "/p", "codex")
	addMsg(t, d, "cx1", 1, "user", "authentication bug repro")

	m := resultJSON(t, call(t, handleAsk(d, nil), map[string]any{"question": "authentication bug"}))
	groups, _ := m["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group filtered to the registry default, got %d: %v", len(groups), groups)
	}
	g, _ := groups[0].(map[string]any)
	if g["session_uuid"] != "cd1" {
		t.Errorf("expected the custom-default session's group, got %v", g)
	}
}

func TestHandleReadSessionDefaultSourceComesFromRegistry(t *testing.T) {
	withFakeDefaultSource(t)
	d := testDB(t)
	// Non-exact prefix match: ResolvePrefix's exact-match fast path ignores
	// source entirely, so use a uuid the "source" arg's default must
	// disambiguate via the prefix-match path (sessions.go:171-174).
	addSessionWithSource(t, d, "cd1-full", "/p", "custom-default")
	addSessionWithSource(t, d, "cx1-full", "/p", "codex")

	r := call(t, handleReadSession(d, nil), map[string]any{"uuid": "cd1"})
	if r.IsError {
		t.Fatalf("expected the custom-default session to resolve by prefix without an explicit source, got error: %+v", r)
	}
	m := resultJSON(t, r)
	sess, _ := m["session"].(map[string]any)
	if sess["uuid"] != "cd1-full" {
		t.Errorf("expected cd1-full to resolve, got %v", sess)
	}

	// The codex-prefixed session must NOT resolve without an explicit source,
	// proving the default really did scope the lookup (not "all").
	r2 := call(t, handleReadSession(d, nil), map[string]any{"uuid": "cx1"})
	if !r2.IsError {
		t.Fatal("expected the codex session to be out of scope under the registry default source")
	}
}

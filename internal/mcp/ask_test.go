package mcp

import (
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

// addSessionWithSource inserts a session with an explicit source column, for
// tests that need a mix of providers (addSession in tools_test.go leaves source
// NULL, which every read path treats as "claude-code").
func addSessionWithSource(t *testing.T, d *db.DB, uuid, project, source string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, ended_at, turn_count, source) VALUES (?,?,?,?,1,?)`,
		uuid, project, uuid+".jsonl", time.Now().Unix(), source); err != nil {
		t.Fatal(err)
	}
}

func TestHandleAskRequiresQuestion(t *testing.T) {
	d := testDB(t)
	r := call(t, handleAsk(d, nil), map[string]any{})
	if !r.IsError {
		t.Fatal("expected error result when question missing")
	}
}

func TestHandleAskRejectsEmptyQuestion(t *testing.T) {
	d := testDB(t)
	r := call(t, handleAsk(d, nil), map[string]any{"question": "   "})
	if !r.IsError {
		t.Fatal("expected an error result for a blank question")
	}
}

func TestHandleAskReturnsCitedBundle(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/x")
	addMsg(t, d, "s1", 0, "user", "we keep hitting an authentication failure")
	addMsg(t, d, "s1", 1, "assistant", "rotate the token to fix it")

	r := call(t, handleAsk(d, nil), map[string]any{"question": "how did we fix authentication?"})
	m := resultJSON(t, r)
	if int(m["count"].(float64)) != 1 {
		t.Fatalf("count=%v want 1", m["count"])
	}
	groups := m["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	g := groups[0].(map[string]any)
	if g["session_uuid"] != "s1" {
		t.Fatalf("group session_uuid=%v want s1", g["session_uuid"])
	}
	excerpts := g["excerpts"].([]any)
	if len(excerpts) == 0 {
		t.Fatal("expected excerpts in the group")
	}
	hit := false
	for _, e := range excerpts {
		if e.(map[string]any)["is_hit"] == true {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("expected a hit-marked excerpt: %v", excerpts)
	}
}

// TestHandleAskSourceAllTagsGroupsBySource guards the codex P2: ask --source all
// mixes evidence from multiple providers into one bundle, but the MCP response had
// no per-group source field (unlike search/list) — a cross-provider citation was
// unattributable. Each group must carry the source of the session it came from.
func TestHandleAskSourceAllTagsGroupsBySource(t *testing.T) {
	d := testDB(t)
	addSessionWithSource(t, d, "cc1", "/p", "claude-code")
	addMsg(t, d, "cc1", 0, "user", "we hit an authentication bug in claude code")
	addSessionWithSource(t, d, "cx1", "/p", "codex")
	addMsg(t, d, "cx1", 0, "user", "we hit an authentication bug in codex")

	r := call(t, handleAsk(d, nil), map[string]any{"question": "authentication bug", "source": "all"})
	m := resultJSON(t, r)
	groups := m["groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups (one per source), got %d: %v", len(groups), groups)
	}
	bySession := map[string]string{}
	for _, gv := range groups {
		g := gv.(map[string]any)
		src, _ := g["source"].(string) // absent (nil) becomes "" rather than panicking
		bySession[g["session_uuid"].(string)] = src
	}
	if bySession["cc1"] != "claude-code" {
		t.Fatalf("cc1 group source=%q want claude-code: %v", bySession["cc1"], bySession)
	}
	if bySession["cx1"] != "codex" {
		t.Fatalf("cx1 group source=%q want codex: %v", bySession["cx1"], bySession)
	}
}

package mcp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/ask"
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

// TestHandleAskMaxTokensBoundsBundle guards the ask tool's max_tokens param:
// a tiny budget on a session whose hits far exceed it must still return a
// non-empty bundle (the keep-top-hits invariant), governed by the effective
// bound rather than an untouched raw budget.
func TestHandleAskMaxTokensBoundsBundle(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "big", "/p")
	filler := strings.Repeat("填充內容測試文字說明範例持續", 6) // 84 CJK runes, no query term
	content := "資料庫遷移" + filler                   // 89 CJK runes; all 3 msgs match the query
	for i := 0; i < 3; i++ {
		addMsg(t, d, "big", i, "user", content)
	}

	r := call(t, handleAsk(d, nil), map[string]any{"question": "資料庫遷移", "max_tokens": minMaxTokens})
	m := resultJSON(t, r)
	groups, ok := m["groups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("want 1 group, got %v", m["groups"])
	}
	total := 0
	for _, gv := range groups {
		g := gv.(map[string]any)
		for _, ev := range g["excerpts"].([]any) {
			e := ev.(map[string]any)
			total += ask.EstimateTokens(e["text"].(string))
		}
	}
	if total == 0 {
		t.Fatalf("expected a non-empty bundle within the keep-top-hits invariant, got 0 tokens")
	}
}

// Omitting max_tokens must apply the same default as passing it explicitly.
func TestHandleAskMaxTokensDefaultsWhenOmitted(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addMsg(t, d, "s1", 0, "user", "we keep hitting an authentication failure")

	omitted := call(t, handleAsk(d, nil), map[string]any{"question": "authentication failure"})
	explicit := call(t, handleAsk(d, nil), map[string]any{"question": "authentication failure", "max_tokens": defaultMaxTokens})
	if !reflect.DeepEqual(resultJSON(t, omitted), resultJSON(t, explicit)) {
		t.Fatalf("omitting max_tokens should match passing the explicit default (%d)", defaultMaxTokens)
	}
}

// TestHandleAskMaxTokensThreadsThroughToGroupCount is the key wiring guard:
// a request param that never reaches ask.Options.MaxTokens would make every
// call fall back to the package default (2000) regardless of what's passed,
// so this asserts a value far above the default (clamped to maxMaxTokens)
// actually changes how many of 4 equal-sized, budget-straddling groups come
// back, compared to the default. Each group's single excerpt is dense CJK
// content long enough to saturate the 600-rune per-excerpt cap (~600 tokens,
// 1:1) — ASCII content can never exceed that per-excerpt cap densely enough
// to straddle the 2000 budget within the 6-session default MaxSessions.
func TestHandleAskMaxTokensThreadsThroughToGroupCount(t *testing.T) {
	d := testDB(t)
	filler := strings.Repeat("填充內容測試文字說明範例持續資料流程細節描述完整", 30) // >600 CJK runes
	content := "資料庫遷移" + filler
	for _, uuid := range []string{"s1", "s2", "s3", "s4"} {
		addSession(t, d, uuid, "/p")
		addMsg(t, d, uuid, 0, "user", content)
	}

	def := call(t, handleAsk(d, nil), map[string]any{"question": "資料庫遷移"})
	defGroups := resultJSON(t, def)["groups"].([]any)

	over := call(t, handleAsk(d, nil), map[string]any{"question": "資料庫遷移", "max_tokens": 999999})
	overGroups := resultJSON(t, over)["groups"].([]any)

	if len(defGroups) != 3 {
		t.Fatalf("setup: default budget (%d) should keep exactly 3 of the 4 ~601-token groups, got %d", defaultMaxTokens, len(defGroups))
	}
	if len(overGroups) != 4 {
		t.Fatalf("max_tokens=999999 (clamped to %d) should keep all 4 groups, got %d — max_tokens is not reaching ask.Options.MaxTokens",
			maxMaxTokens, len(overGroups))
	}
}

// TestHandleAskMaxTokensBelowMinIsRaisedToFloor guards the true-minimum half
// of clamping (distinct from the default-when-omitted path): a *positive*
// max_tokens under minMaxTokens must be raised to minMaxTokens, not passed
// through as a near-zero budget. A group with one hit message plus one
// windowed non-hit neighbor is kept whole at the floor (200) but collapses to
// hits-only at an unclamped 1.
func TestHandleAskMaxTokensBelowMinIsRaisedToFloor(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	addMsg(t, d, "s1", 0, "user", "we keep hitting an authentication failure")
	addMsg(t, d, "s1", 1, "assistant", "investigating now")

	under := call(t, handleAsk(d, nil), map[string]any{"question": "authentication failure", "max_tokens": 1})
	atMin := call(t, handleAsk(d, nil), map[string]any{"question": "authentication failure", "max_tokens": minMaxTokens})
	if !reflect.DeepEqual(resultJSON(t, under), resultJSON(t, atMin)) {
		t.Fatalf("max_tokens=1 should be raised to the floor (%d) and match an explicit call at the floor, got a different bundle", minMaxTokens)
	}
}

func TestClampRange(t *testing.T) {
	cases := []struct {
		name                   string
		v, def, min, max, want int
	}{
		{"non-positive defaults", 0, 2000, 200, 8000, 2000},
		{"negative defaults", -5, 2000, 200, 8000, 2000},
		{"below min raised to floor", 1, 2000, 200, 8000, 200},
		{"below min raised to floor (199)", 199, 2000, 200, 8000, 200},
		{"at min unchanged", 200, 2000, 200, 8000, 200},
		{"in range unchanged", 500, 2000, 200, 8000, 500},
		{"at max unchanged", 8000, 2000, 200, 8000, 8000},
		{"above max clamped", 999999, 2000, 200, 8000, 8000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clampRange(c.v, c.def, c.min, c.max); got != c.want {
				t.Errorf("clampRange(%d,%d,%d,%d)=%d want %d", c.v, c.def, c.min, c.max, got, c.want)
			}
		})
	}
}

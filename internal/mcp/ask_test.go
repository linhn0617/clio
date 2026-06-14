package mcp

import (
	"testing"
)

func TestHandleAskRequiresQuestion(t *testing.T) {
	d := testDB(t)
	r := call(t, handleAsk(d, nil), map[string]any{})
	if !r.IsError {
		t.Fatal("expected error result when question missing")
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

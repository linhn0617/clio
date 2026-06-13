package claudeconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func sessionStartGroups(t *testing.T, root map[string]any) []any {
	t.Helper()
	hooks, _ := root["hooks"].(map[string]any)
	ss, _ := hooks["SessionStart"].([]any)
	return ss
}

func TestAddSessionStartHookPreservesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{
		"theme":"dark",
		"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/opt/security-scan.sh"}]}]}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := AddSessionStartHook(path, "/Users/lin/go/bin/clio recall"); err != nil {
		t.Fatal(err)
	}
	if has, _ := HasSessionStartHook(path); !has {
		t.Fatal("expected clio hook present after add")
	}
	root := readSettings(t, path)
	if root["theme"] != "dark" {
		t.Error("unrelated key 'theme' not preserved")
	}
	ss := sessionStartGroups(t, root)
	if len(ss) != 2 {
		t.Fatalf("expected 2 SessionStart groups (existing + clio), got %d", len(ss))
	}
	found := false
	for _, g := range ss {
		for _, h := range g.(map[string]any)["hooks"].([]any) {
			if h.(map[string]any)["command"] == "/opt/security-scan.sh" {
				found = true
			}
		}
	}
	if !found {
		t.Error("existing security-scan hook was lost")
	}
}

func TestAddSessionStartHookIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := AddSessionStartHook(path, "/x/clio recall"); err != nil {
		t.Fatal(err)
	}
	if err := AddSessionStartHook(path, "/x/clio recall"); err != nil {
		t.Fatal(err)
	}
	if ss := sessionStartGroups(t, readSettings(t, path)); len(ss) != 1 {
		t.Fatalf("expected 1 group after double add, got %d", len(ss))
	}
}

func TestRemoveSessionStartHookKeepsCoGroupedHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// clio's hook shares a single group with an unrelated hook.
	if err := os.WriteFile(path, []byte(`{"hooks":{"SessionStart":[
		{"hooks":[
			{"type":"command","command":"/opt/security-scan.sh"},
			{"type":"command","command":"/x/clio recall"}
		]}
	]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSessionStartHook(path); err != nil {
		t.Fatal(err)
	}
	root := readSettings(t, path)
	ss := sessionStartGroups(t, root)
	if len(ss) != 1 {
		t.Fatalf("expected the group to remain (security-scan stays), got %d groups", len(ss))
	}
	hs := ss[0].(map[string]any)["hooks"].([]any)
	if len(hs) != 1 || hs[0].(map[string]any)["command"] != "/opt/security-scan.sh" {
		t.Fatalf("expected only the security-scan hook to remain, got %v", hs)
	}
}

func TestIsClioRecallDoesNotFalseMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// Commands that contain "clio" and end in "recall" but are NOT clio's hook.
	if err := os.WriteFile(path, []byte(`{"hooks":{"SessionStart":[
		{"hooks":[{"type":"command","command":"/usr/bin/clio-helper recall"}]},
		{"hooks":[{"type":"command","command":"myclio recall"}]}
	]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if has, _ := HasSessionStartHook(path); has {
		t.Fatal("clio-helper/myclio recall must NOT be identified as clio's hook")
	}
}

func TestRemoveSessionStartHookKeepsOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"SessionStart":[
		{"hooks":[{"type":"command","command":"/opt/security-scan.sh"}]},
		{"hooks":[{"type":"command","command":"/x/clio recall"}]}
	]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSessionStartHook(path); err != nil {
		t.Fatal(err)
	}
	if has, _ := HasSessionStartHook(path); has {
		t.Fatal("clio hook should be gone after remove")
	}
	ss := sessionStartGroups(t, readSettings(t, path))
	if len(ss) != 1 {
		t.Fatalf("expected 1 remaining group, got %d", len(ss))
	}
	if ss[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"] != "/opt/security-scan.sh" {
		t.Error("removed the wrong hook")
	}
}

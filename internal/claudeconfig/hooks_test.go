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

func preToolUseGroups(t *testing.T, root map[string]any) []any {
	t.Helper()
	hooks, _ := root["hooks"].(map[string]any)
	pt, _ := hooks["PreToolUse"].([]any)
	return pt
}

func TestAddPreToolUseReadHookPreservesExistingGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{
		"hooks":{"PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"/opt/read-gate.sh"}]}],
		         "SessionStart":[{"hooks":[{"type":"command","command":"/Users/lin/go/bin/clio recall"}]}]}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddPreToolUseReadHook(path, "/Users/lin/go/bin/clio file-history --hook"); err != nil {
		t.Fatal(err)
	}
	if has, _ := HasPreToolUseReadHook(path); !has {
		t.Fatal("expected file-history hook present after add")
	}
	root := readSettings(t, path)
	pt := preToolUseGroups(t, root)
	if len(pt) != 2 {
		t.Fatalf("expected the user's Read gate group + clio's group, got %d", len(pt))
	}
	var clio map[string]any
	for _, g := range pt {
		gm := g.(map[string]any)
		for _, h := range gm["hooks"].([]any) {
			hm := h.(map[string]any)
			switch hm["command"] {
			case "/opt/read-gate.sh":
				// preserved
			case "/Users/lin/go/bin/clio file-history --hook":
				clio = gm
				if hm["timeout"] != float64(10) && hm["timeout"] != json.Number("10") {
					t.Errorf("clio hook should carry timeout 10, got %#v", hm["timeout"])
				}
			}
		}
	}
	if clio == nil || clio["matcher"] != "Read" {
		t.Fatalf("clio group missing or matcher != Read: %#v", clio)
	}
	if len(sessionStartGroups(t, root)) != 1 {
		t.Error("SessionStart must be untouched by the PreToolUse add")
	}
}

func TestAddPreToolUseReadHookIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := AddPreToolUseReadHook(path, "/x/clio file-history --hook"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(preToolUseGroups(t, readSettings(t, path))); n != 1 {
		t.Fatalf("expected exactly 1 PreToolUse group after two adds, got %d", n)
	}
}

func TestRemovePreToolUseReadHookKeepsOthersAndDeletesEmptyKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// clio's entry co-grouped with a foreign Read hook, plus a foreign-only group.
	if err := os.WriteFile(path, []byte(`{
		"hooks":{"PreToolUse":[
			{"matcher":"Read","hooks":[{"type":"command","command":"/opt/read-gate.sh"},{"type":"command","command":"/x/clio file-history --hook","timeout":10}]},
			{"matcher":"Bash","hooks":[{"type":"command","command":"/opt/bash-gate.sh"}]}
		]}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemovePreToolUseReadHook(path); err != nil {
		t.Fatal(err)
	}
	root := readSettings(t, path)
	pt := preToolUseGroups(t, root)
	if len(pt) != 2 {
		t.Fatalf("both foreign groups must survive, got %d", len(pt))
	}
	for _, g := range pt {
		for _, h := range g.(map[string]any)["hooks"].([]any) {
			if cmd, _ := h.(map[string]any)["command"].(string); isClioFileHistory(cmd) {
				t.Errorf("clio entry still present: %s", cmd)
			}
		}
	}
	if has, _ := HasPreToolUseReadHook(path); has {
		t.Error("HasPreToolUseReadHook should be false after remove")
	}

	// Only clio's entry in the only group → the PreToolUse key disappears entirely.
	path2 := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path2, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"/x/clio file-history --hook"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemovePreToolUseReadHook(path2); err != nil {
		t.Fatal(err)
	}
	hooks, _ := readSettings(t, path2)["hooks"].(map[string]any)
	if _, still := hooks["PreToolUse"]; still {
		t.Error("emptied PreToolUse key should be deleted")
	}
}

// An entry parked under another matcher does not count as installed (it would
// never fire for Read); removal still clears it wherever it sits.
func TestAddPreToolUseReadHookIgnoresWrongMatcher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/x/clio file-history --hook"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if has, _ := HasPreToolUseReadHook(path); has {
		t.Fatal("an entry under matcher Bash must not count as installed")
	}
	if err := AddPreToolUseReadHook(path, "/x/clio file-history --hook"); err != nil {
		t.Fatal(err)
	}
	pt := preToolUseGroups(t, readSettings(t, path))
	if len(pt) != 2 || pt[1].(map[string]any)["matcher"] != "Read" {
		t.Fatalf("expected a new Read group alongside the Bash one, got %#v", pt)
	}
	if err := RemoveClioHooks(path); err != nil {
		t.Fatal(err)
	}
	if _, still := readSettings(t, path)["hooks"]; still {
		t.Error("both entries removed → no groups → no hooks key left")
	}
}

// --no-file-context (empty fileHistoryCmd) removes a previously installed hook.
func TestAddClioHooksEmptyFileHistoryRemovesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := AddClioHooks(path, "/x/clio recall", "/x/clio file-history --hook"); err != nil {
		t.Fatal(err)
	}
	if err := AddClioHooks(path, "/x/clio recall", ""); err != nil {
		t.Fatal(err)
	}
	root := readSettings(t, path)
	if has, _ := HasPreToolUseReadHook(path); has || len(preToolUseGroups(t, root)) != 0 {
		t.Fatal("opting out must remove the file-history hook")
	}
	if len(sessionStartGroups(t, root)) != 1 {
		t.Error("recall hook must stay")
	}
}

func TestIsClioFileHistoryShape(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/Users/lin/go/bin/clio file-history --hook": true,
		"clio file-history --hook":                   true,
		"clio-helper file-history --hook":            false,
		"clio file-history":                          false, // CLI form, not the hook
		"clio file-history --hook extra":             false,
		"clio recall":                                false,
	} {
		if got := isClioFileHistory(cmd); got != want {
			t.Errorf("isClioFileHistory(%q) = %v, want %v", cmd, got, want)
		}
	}
}

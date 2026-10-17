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

func TestIsClioHookCommandsQuotedPaths(t *testing.T) {
	for cmd, want := range map[string]bool{
		`'/Users/me/My Tools/clio' recall`:              true,
		`"/Users/me/My Tools/clio" recall`:              true,
		`'/Users/me/My Tools/clio' file-history --hook`: false, // wrong kind for recall
		`'/a/it'\''s here/clio' recall`:                 true,
		`'/Users/me/My Tools/clio-helper' recall`:       false,
		`'/Users/me/My Tools/clio recall`:               false, // unterminated quote
		`/Users/me/My Tools/clio recall`:                true,  // the old unquoted, broken shape an older install wrote
		`/Users/me/My Tools/clio-helper recall`:         false,
		`echo /x/clio recall`:                           false, // not a bare absolute path
		`/bin/x; /x/clio recall`:                        false, // shell metacharacter, not a path
		`/usr/bin/env /opt/clio recall`:                 false, // a wrapper, not a split path
		`/usr/bin/env ~/bin/clio recall`:                false,
		`/usr/bin/env ./bin/clio recall`:                false, // relative wrapper argument
		`/Users/me/My Apps/bin/clio recall`:             true,  // a split path whose later piece contains /
	} {
		if got := isClioRecall(cmd); got != want {
			t.Errorf("isClioRecall(%q) = %v, want %v", cmd, got, want)
		}
	}
	for cmd, want := range map[string]bool{
		`'/Users/me/My Tools/clio' file-history --hook`: true,
		`"/Users/me/My Tools/clio" file-history --hook`: true,
		`'/Users/me/My Tools/clio' file-history`:        false,
		`/Users/me/My Tools/clio file-history --hook`:   true, // old unquoted, broken shape
		`/Users/me/My Tools/clio file-history`:          false,
		`/usr/bin/sudo /opt/clio file-history --hook`:   false, // a wrapper, not a split path
		`/usr/bin/sudo ../opt/clio file-history --hook`: false, // relative wrapper argument
	} {
		if got := isClioFileHistory(cmd); got != want {
			t.Errorf("isClioFileHistory(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/lin/go/bin/clio": "/Users/lin/go/bin/clio", // plain path unchanged
		"clio":                   "clio",
		"/a-b_c.d/clio+x@y:z":    "/a-b_c.d/clio+x@y:z",
		"/My Tools/clio":         "'/My Tools/clio'",
		"/it's/clio":             `'/it'\''s/clio'`,
		"/a$b/clio":              "'/a$b/clio'",
		"/a&b/clio":              "'/a&b/clio'",
		"/a\\b/clio":             "'/a\\b/clio'",
		"/tab\t/clio":            "'/tab\t/clio'",
	} {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
		toks, ok := splitCommand(ShellQuote(in) + " recall")
		if !ok || len(toks) != 2 || toks[0] != in {
			t.Errorf("round trip of %q gave %q ok=%v", in, toks, ok)
		}
	}
}

func TestInstallQuotedPathRegistersOnceAndUninstalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	exe := ShellQuote("/Users/me/My Tools/clio")
	for i := 0; i < 2; i++ {
		if err := AddClioHooks(path, exe+" recall", exe+" file-history --hook"); err != nil {
			t.Fatal(err)
		}
	}
	root := readSettings(t, path)
	if n := len(recallGroups(t, root)); n != 1 {
		t.Fatalf("recall groups after re-install = %d, want 1", n)
	}
	if err := RemoveClioHooks(path); err != nil {
		t.Fatal(err)
	}
	if has, _ := HasSessionStartHook(path); has {
		t.Fatal("uninstall left the quoted recall hook")
	}
	if has, _ := HasPreToolUseReadHook(path); has {
		t.Fatal("uninstall left the quoted file-history hook")
	}
}

// recallGroups returns the SessionStart groups holding clio's recall entry.
func recallGroups(t *testing.T, root map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, g := range sessionStartGroups(t, root) {
		if groupIsClioRecall(g) {
			out = append(out, g.(map[string]any))
		}
	}
	return out
}

func clioRecallEntry(t *testing.T, g map[string]any) map[string]any {
	t.Helper()
	for _, h := range g["hooks"].([]any) {
		if hookIsClioRecall(h) {
			return h.(map[string]any)
		}
	}
	t.Fatal("group has no clio recall entry")
	return nil
}

// The recall hook skips `fork` (it inherits the parent's context) and carries a
// short timeout so a cold-cache index can never stall startup for 600 s.
func TestAddSessionStartHookWritesMatcherAndTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := AddSessionStartHook(path, "/x/clio recall"); err != nil {
		t.Fatal(err)
	}
	gs := recallGroups(t, readSettings(t, path))
	if len(gs) != 1 {
		t.Fatalf("want 1 recall group, got %d", len(gs))
	}
	if gs[0]["matcher"] != "startup|resume|clear|compact" {
		t.Errorf("matcher = %v", gs[0]["matcher"])
	}
	if to := clioRecallEntry(t, gs[0])["timeout"]; to != float64(10) {
		t.Errorf("timeout = %v, want 10", to)
	}
}

// An entry written by an older clio (no matcher, no timeout) is upgraded where
// it sits, not duplicated.
func TestAddSessionStartHookUpgradesLegacyInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"SessionStart":[
		{"hooks":[{"type":"command","command":"/opt/scan.sh"}]},
		{"hooks":[{"type":"command","command":"/old/bin/clio recall"}]}
	]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if legacy, _ := HasLegacySessionStartHook(path); !legacy {
		t.Fatal("legacy shape should be detected before upgrade")
	}
	if err := AddSessionStartHook(path, "/new/bin/clio recall"); err != nil {
		t.Fatal(err)
	}
	root := readSettings(t, path)
	ss := sessionStartGroups(t, root)
	if len(ss) != 2 {
		t.Fatalf("want 2 groups (foreign + upgraded clio), got %d: %v", len(ss), ss)
	}
	if _, has := ss[0].(map[string]any)["matcher"]; has {
		t.Error("the foreign group must stay untouched")
	}
	g := ss[1].(map[string]any)
	if g["matcher"] != "startup|resume|clear|compact" || clioRecallEntry(t, g)["timeout"] != float64(10) {
		t.Errorf("legacy group not upgraded in place: %v", g)
	}
	if c := clioRecallEntry(t, g)["command"]; c != "/new/bin/clio recall" {
		t.Errorf("in-place upgrade kept the old binary path: %v", c)
	}
	if legacy, _ := HasLegacySessionStartHook(path); legacy {
		t.Error("no legacy entry should remain")
	}
	// A second run is a no-op.
	if err := AddSessionStartHook(path, "/new/bin/clio recall"); err != nil {
		t.Fatal(err)
	}
	if n := len(sessionStartGroups(t, readSettings(t, path))); n != 2 {
		t.Errorf("re-run changed the group count to %d", n)
	}
}

// A legacy clio entry co-grouped with a foreign hook moves to its own matched
// group; the foreign hook keeps firing on every event as before.
func TestAddSessionStartHookUpgradesCoGroupedLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"SessionStart":[
		{"hooks":[{"type":"command","command":"/opt/scan.sh"},{"type":"command","command":"/x/clio recall"}]}
	]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddSessionStartHook(path, "/x/clio recall"); err != nil {
		t.Fatal(err)
	}
	root := readSettings(t, path)
	ss := sessionStartGroups(t, root)
	if len(ss) != 2 {
		t.Fatalf("want foreign group + clio group, got %v", ss)
	}
	first := ss[0].(map[string]any)
	if _, has := first["matcher"]; has || len(first["hooks"].([]any)) != 1 || groupIsClioRecall(first) {
		t.Errorf("foreign group should keep only its own hook, unmatched: %v", first)
	}
	gs := recallGroups(t, root)
	if len(gs) != 1 || gs[0]["matcher"] != "startup|resume|clear|compact" {
		t.Errorf("want exactly one matched recall group, got %v", gs)
	}
}

// A recall entry the user placed under their own matcher is theirs: left as is.
func TestAddSessionStartHookKeepsCustomMatcher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	orig := `{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"/x/clio recall"}]}]}}`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddSessionStartHook(path, "/x/clio recall"); err != nil {
		t.Fatal(err)
	}
	gs := recallGroups(t, readSettings(t, path))
	if len(gs) != 1 || gs[0]["matcher"] != "startup" {
		t.Errorf("custom matcher should be kept and nothing added, got %v", gs)
	}
}

// Removal recognises both the legacy and the matched shape.
func TestRemoveClioHooksRemovesBothRecallShapes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"SessionStart":[
		{"hooks":[{"type":"command","command":"/old/clio recall"}]},
		{"matcher":"startup|resume|clear|compact","hooks":[{"type":"command","command":"/new/clio recall","timeout":10}]}
	]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if recall, _, _ := HasAnyClioHookEntries(path); !recall {
		t.Fatal("both shapes should be recognised")
	}
	if err := RemoveClioHooks(path); err != nil {
		t.Fatal(err)
	}
	if recall, _, _ := HasAnyClioHookEntries(path); recall {
		t.Error("a recall entry survived removal")
	}
}

// An older install wrote the binary path unquoted, so a path with a space split
// into extra words: install must upgrade that entry in place, not add a second
// one, and uninstall must remove it.
func TestInstallUpgradesUnquotedSpacePathAndUninstalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{
		"SessionStart":[{"matcher":"startup|resume|clear|compact","hooks":[{"type":"command","command":"/Users/me/My Tools/clio recall","timeout":10}]}],
		"PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"/Users/me/My Tools/clio file-history --hook","timeout":10}]}]
	}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := ShellQuote("/Users/me/My Tools/clio")
	if err := AddClioHooks(path, exe+" recall", exe+" file-history --hook"); err != nil {
		t.Fatal(err)
	}
	root := readSettings(t, path)
	ss := sessionStartGroups(t, root)
	if len(ss) != 1 {
		t.Fatalf("SessionStart groups = %d, want 1: %v", len(ss), ss)
	}
	if c := clioRecallEntry(t, ss[0].(map[string]any))["command"]; c != `'/Users/me/My Tools/clio' recall` {
		t.Errorf("recall command = %v, want the quoted form", c)
	}
	pt := preToolUseGroups(t, root)
	if len(pt) != 1 {
		t.Fatalf("PreToolUse groups = %d, want 1: %v", len(pt), pt)
	}
	h := pt[0].(map[string]any)["hooks"].([]any)
	if len(h) != 1 || h[0].(map[string]any)["command"] != `'/Users/me/My Tools/clio' file-history --hook` {
		t.Errorf("file-history hooks = %v, want one quoted entry", h)
	}
	if err := RemoveClioHooks(path); err != nil {
		t.Fatal(err)
	}
	if _, has := readSettings(t, path)["hooks"]; has {
		t.Errorf("uninstall left hooks behind: %v", readSettings(t, path))
	}
}

// Uninstall also removes a broken unquoted entry without an install in between.
func TestRemoveClioHooksRemovesUnquotedSpacePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/Users/me/My Tools/clio recall"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveClioHooks(path); err != nil {
		t.Fatal(err)
	}
	if _, has := readSettings(t, path)["hooks"]; has {
		t.Errorf("uninstall left the broken entry: %v", readSettings(t, path))
	}
}

// A foreign wrapper around clio (env, sudo) is someone else's hook: install must
// neither rewrite it nor count it as ours, and uninstall must keep it.
func TestWrapperCommandsAreNotClioHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{
		"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"/usr/bin/env /opt/clio recall"}]}],
		"PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"/usr/bin/sudo /opt/clio file-history --hook"}]}]
	}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddClioHooks(path, "/new/clio recall", "/new/clio file-history --hook"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveClioHooks(path); err != nil {
		t.Fatal(err)
	}
	root := readSettings(t, path)
	ss := sessionStartGroups(t, root)
	pt := preToolUseGroups(t, root)
	if len(ss) != 1 || len(pt) != 1 {
		t.Fatalf("wrapper hooks not kept: SessionStart=%v PreToolUse=%v", ss, pt)
	}
	if c := ss[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"]; c != "/usr/bin/env /opt/clio recall" {
		t.Errorf("SessionStart wrapper rewritten to %v", c)
	}
	if c := pt[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"]; c != "/usr/bin/sudo /opt/clio file-history --hook" {
		t.Errorf("PreToolUse wrapper rewritten to %v", c)
	}
}

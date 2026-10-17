package claudeconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ClioHookCommands returns the binary path the recogniser resolved, not the whole
// command, so doctor never re-parses a command line itself.
func TestClioHookBinaries(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "none.json")
	if r, f, err := ClioHookCommands(missing); err != nil || len(r) != 0 || len(f) != 0 {
		t.Fatalf("missing settings: want empty, got %v %v err=%v", r, f, err)
	}

	p := filepath.Join(dir, "settings.json")
	writeFile(t, p, `{"hooks":{
	  "SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"/opt/bin/clio recall"},{"type":"command","command":"/usr/bin/other recall"}]}],
	  "PreToolUse":[{"matcher":"Read|Bash","hooks":[{"type":"command","command":"'/Users/a b/clio' file-history --hook"}]}]
	}}`)
	r, f, err := ClioHookCommands(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, []string{"/opt/bin/clio"}) {
		t.Fatalf("recall binaries = %v", r)
	}
	if !reflect.DeepEqual(f, []string{"/Users/a b/clio"}) {
		t.Fatalf("file-history binaries = %v", f)
	}

	writeFile(t, p, `{not json`)
	if _, _, err := ClioHookCommands(p); err == nil {
		t.Fatal("malformed settings must return an error")
	}
}

// The pre-quoting shape (absolute path with a space, unquoted) must resolve to the
// full path, not to its first whitespace-separated piece.
func TestClioHookBinariesLegacyUnquotedWithSpace(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeFile(t, p, `{"hooks":{
	  "SessionStart":[{"hooks":[{"type":"command","command":"/Users/a b/bin/clio recall"}]}],
	  "PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"/Users/a b/bin/clio file-history --hook"}]}]}}`)
	r, f, err := ClioHookCommands(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, []string{"/Users/a b/bin/clio"}) || !reflect.DeepEqual(f, []string{"/Users/a b/bin/clio"}) {
		t.Fatalf("legacy unquoted: recall=%v file-history=%v", r, f)
	}
}

// Windows installs write clio.exe; the recognisers must accept it (and still reject
// look-alikes).
func TestRecognisersAcceptExeSuffix(t *testing.T) {
	for _, c := range []string{`/opt/bin/clio.exe recall`, `'/opt/my bin/clio.exe' recall`} {
		if !isClioRecall(c) {
			t.Errorf("isClioRecall(%q) = false", c)
		}
	}
	if !isClioFileHistory(`/opt/bin/clio.exe file-history --hook`) {
		t.Error("isClioFileHistory must accept clio.exe")
	}
	for _, c := range []string{`/opt/bin/clio.exe.bak recall`, `/opt/bin/clio-helper.exe recall`, `/opt/bin/xclio.exe recall`} {
		if isClioRecall(c) {
			t.Errorf("isClioRecall(%q) = true, want false", c)
		}
	}
}

func TestLookupServerStates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude.json")
	cases := []struct {
		name, body string
		kind       ServerKind
		cmd        string
	}{
		{"no mcpServers", `{}`, ServerAbsent, ""},
		{"other servers only", `{"mcpServers":{"other":{"command":"/x"}}}`, ServerAbsent, ""},
		{"local command", `{"mcpServers":{"clio":{"command":"/opt/bin/clio","args":["mcp"]}}}`, ServerLocal, "/opt/bin/clio"},
		{"url based", `{"mcpServers":{"clio":{"type":"sse","url":"http://x/y"}}}`, ServerRemote, ""},
		{"null entry", `{"mcpServers":{"clio":null}}`, ServerMalformed, ""},
		{"string entry", `{"mcpServers":{"clio":"oops"}}`, ServerMalformed, ""},
		{"command not a string", `{"mcpServers":{"clio":{"command":7}}}`, ServerMalformed, ""},
		{"empty command", `{"mcpServers":{"clio":{"command":""}}}`, ServerMalformed, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeFile(t, p, c.body)
			cmd, kind, err := LookupServer(p, "clio")
			if err != nil || kind != c.kind || cmd != c.cmd {
				t.Fatalf("got cmd=%q kind=%v err=%v, want cmd=%q kind=%v", cmd, kind, err, c.cmd, c.kind)
			}
		})
	}
	if _, kind, err := LookupServer(filepath.Join(dir, "none.json"), "clio"); err != nil || kind != ServerAbsent {
		t.Fatalf("missing file: kind=%v err=%v", kind, err)
	}
	writeFile(t, p, `{"mcpServers":"nope"}`)
	_, _, err := LookupServer(p, "clio")
	if err == nil || strings.Contains(err.Error(), "refusing to modify") {
		t.Fatalf("non-object mcpServers: want a read-only-toned error, got %v", err)
	}
}

package claudeconfig

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
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

// snapshot records name, mode, size, mtime and sha256 of everything under root.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := de.Info()
		if err != nil {
			return err
		}
		sum := ""
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum = fmt.Sprintf("%x", sha256.Sum256(b))
		}
		out[p] = fmt.Sprintf("%v|%d|%d|%s", fi.Mode(), fi.Size(), fi.ModTime().UnixNano(), sum)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// readOnly runs fn and fails if anything under dir was created, removed or changed
// (a .bak, a temp file from an atomic write, a chmod).
func readOnly(t *testing.T, dir string, fn func()) {
	t.Helper()
	before := snapshot(t, dir)
	fn()
	if after := snapshot(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("directory changed:\nbefore %v\nafter  %v", before, after)
	}
}

// ClioHookCommands returns the binary path the recogniser resolved, not the whole
// command, so doctor never re-parses a command line itself.
func TestClioHookBinaries(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "none.json")
	readOnly(t, dir, func() {
		if r, f, err := ClioHookCommands(missing); err != nil || len(r) != 0 || len(f) != 0 {
			t.Fatalf("missing settings: want empty, got %v %v err=%v", r, f, err)
		}
	})

	p := filepath.Join(dir, "settings.json")
	writeFile(t, p, `{"hooks":{
	  "SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"/opt/bin/clio recall"},{"type":"command","command":"/usr/bin/other recall"}]}],
	  "PreToolUse":[{"matcher":"Read|Bash","hooks":[{"type":"command","command":"'/Users/a b/clio' file-history --hook"}]}]
	}}`)
	var r, f []HookEntry
	readOnly(t, dir, func() {
		var err error
		if r, f, err = ClioHookCommands(p); err != nil {
			t.Fatal(err)
		}
	})
	if !reflect.DeepEqual(r, []HookEntry{{Binary: "/opt/bin/clio"}}) {
		t.Fatalf("recall entries = %+v", r)
	}
	if !reflect.DeepEqual(f, []HookEntry{{Binary: "/Users/a b/clio"}}) {
		t.Fatalf("file-history entries = %+v", f)
	}

	writeFile(t, p, `{not json`)
	readOnly(t, dir, func() {
		if _, _, err := ClioHookCommands(p); err == nil {
			t.Fatal("malformed settings must return an error")
		}
	})
}

// The pre-quoting shape (absolute path with a space, unquoted) must resolve to the
// full path, not to its first whitespace-separated piece, and be marked Unquoted:
// the shell splits it, so that hook cannot run. The older "Read" matcher on the
// file-history group is marked too.
func TestClioHookBinariesLegacyUnquotedWithSpace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	writeFile(t, p, `{"hooks":{
	  "SessionStart":[{"hooks":[{"type":"command","command":"/Users/a b/bin/clio recall"}]}],
	  "PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"/Users/a b/bin/clio file-history --hook"}]}]}}`)
	var r, f []HookEntry
	readOnly(t, dir, func() {
		var err error
		if r, f, err = ClioHookCommands(p); err != nil {
			t.Fatal(err)
		}
	})
	if !reflect.DeepEqual(r, []HookEntry{{Binary: "/Users/a b/bin/clio", Unquoted: true}}) ||
		!reflect.DeepEqual(f, []HookEntry{{Binary: "/Users/a b/bin/clio", Unquoted: true, OldMatcher: true}}) {
		t.Fatalf("legacy unquoted: recall=%+v file-history=%+v", r, f)
	}
}

// Only the exact older matcher is flagged; Read|Bash and a user's own matcher are not.
func TestClioHookOldMatcher(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	writeFile(t, p, `{"hooks":{"PreToolUse":[
	  {"matcher":" Read ","hooks":[{"type":"command","command":"/a/clio file-history --hook"}]},
	  {"matcher":"Read|Bash","hooks":[{"type":"command","command":"/b/clio file-history --hook"}]},
	  {"matcher":"Bash","hooks":[{"type":"command","command":"/c/clio file-history --hook"}]}]}}`)
	var f []HookEntry
	readOnly(t, dir, func() {
		var err error
		if _, f, err = ClioHookCommands(p); err != nil {
			t.Fatal(err)
		}
	})
	want := []HookEntry{{Binary: "/a/clio", OldMatcher: true}, {Binary: "/b/clio"}, {Binary: "/c/clio"}}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("file-history entries = %+v, want %+v", f, want)
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
			var cmd string
			var kind ServerKind
			var err error
			readOnly(t, dir, func() { cmd, kind, err = LookupServer(p, "clio") })
			if err != nil || kind != c.kind || cmd != c.cmd {
				t.Fatalf("got cmd=%q kind=%v err=%v, want cmd=%q kind=%v", cmd, kind, err, c.cmd, c.kind)
			}
		})
	}
	readOnly(t, dir, func() {
		if _, kind, err := LookupServer(filepath.Join(dir, "none.json"), "clio"); err != nil || kind != ServerAbsent {
			t.Fatalf("missing file: kind=%v err=%v", kind, err)
		}
	})
	writeFile(t, p, `{"mcpServers":"nope"}`)
	var err error
	readOnly(t, dir, func() { _, _, err = LookupServer(p, "clio") })
	if err == nil || strings.Contains(err.Error(), "refusing to modify") {
		t.Fatalf("non-object mcpServers: want a read-only-toned error, got %v", err)
	}
}

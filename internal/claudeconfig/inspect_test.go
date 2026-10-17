package claudeconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClioHookCommands(t *testing.T) {
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
	if !reflect.DeepEqual(r, []string{"/opt/bin/clio recall"}) {
		t.Fatalf("recall commands = %v", r)
	}
	if !reflect.DeepEqual(f, []string{"'/Users/a b/clio' file-history --hook"}) {
		t.Fatalf("file-history commands = %v", f)
	}

	writeFile(t, p, `{not json`)
	if _, _, err := ClioHookCommands(p); err == nil {
		t.Fatal("malformed settings must return an error")
	}
}

func TestServerCommand(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude.json")
	if _, ok, err := ServerCommand(filepath.Join(dir, "none.json"), "clio"); err != nil || ok {
		t.Fatalf("missing config: want ok=false, got ok=%v err=%v", ok, err)
	}
	writeFile(t, p, `{"mcpServers":{"other":{"command":"/x"}}}`)
	if _, ok, err := ServerCommand(p, "clio"); err != nil || ok {
		t.Fatalf("unregistered: want ok=false, got ok=%v err=%v", ok, err)
	}
	writeFile(t, p, `{"mcpServers":{"clio":{"command":"/opt/bin/clio","args":["mcp"]}}}`)
	cmd, ok, err := ServerCommand(p, "clio")
	if err != nil || !ok || cmd != "/opt/bin/clio" {
		t.Fatalf("registered: got %q ok=%v err=%v", cmd, ok, err)
	}
}

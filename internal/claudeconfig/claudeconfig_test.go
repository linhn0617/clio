package claudeconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func read(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("config not valid JSON: %v", err)
	}
	return m
}

func servers(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	s, ok := m["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("no mcpServers map")
	}
	return s
}

func TestAddServerCreatesFileWhenMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	if err := AddServer(p, "clio", ServerEntry{Command: "clio", Args: []string{"mcp"}}); err != nil {
		t.Fatal(err)
	}
	s := servers(t, read(t, p))
	if _, ok := s["clio"]; !ok {
		t.Fatal("clio entry not written")
	}
}

func TestAddServerPreservesExisting(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	// Pre-existing config with another server and an unrelated top-level key.
	orig := `{"mcpServers":{"other":{"command":"other-bin","args":["serve"]}},"theme":"dark"}`
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddServer(p, "clio", ServerEntry{Command: "clio", Args: []string{"mcp"}}); err != nil {
		t.Fatal(err)
	}
	root := read(t, p)
	if root["theme"] != "dark" {
		t.Fatal("unrelated key 'theme' was lost")
	}
	s := servers(t, root)
	if _, ok := s["other"]; !ok {
		t.Fatal("existing 'other' server was lost")
	}
	if _, ok := s["clio"]; !ok {
		t.Fatal("clio server not added")
	}
}

func TestAddServerIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	for range 2 {
		if err := AddServer(p, "clio", ServerEntry{Command: "clio", Args: []string{"mcp"}}); err != nil {
			t.Fatal(err)
		}
	}
	s := servers(t, read(t, p))
	if len(s) != 1 {
		t.Fatalf("expected 1 server after double install, got %d", len(s))
	}
}

func TestRemoveServerKeepsOthers(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	os.WriteFile(p, []byte(`{"mcpServers":{"other":{"command":"x"},"clio":{"command":"clio"}}}`), 0o600)
	if err := RemoveServer(p, "clio"); err != nil {
		t.Fatal(err)
	}
	s := servers(t, read(t, p))
	if _, ok := s["clio"]; ok {
		t.Fatal("clio not removed")
	}
	if _, ok := s["other"]; !ok {
		t.Fatal("other server wrongly removed")
	}
}

func TestNoBackupLeftBehindOnSuccess(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	os.WriteFile(p, []byte(`{"mcpServers":{}}`), 0o600)
	if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".bak"); !os.IsNotExist(err) {
		t.Fatal("backup should be removed after a successful write")
	}
}

func TestBackupRemovedOnRenameFailure(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(p, []byte(`{"mcpServers":{"other":{"command":"x"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.ReadFile(p)
	renameFile = func(string, string) error { return fmt.Errorf("forced rename failure") }
	t.Cleanup(func() { renameFile = os.Rename })
	if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err == nil {
		t.Fatal("expected error from forced rename failure")
	}
	if _, err := os.Stat(p + ".bak"); !os.IsNotExist(err) {
		t.Fatal(".bak must be removed on a failed write")
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(orig) {
		t.Fatalf("original config changed; before=%q after=%q", orig, after)
	}
}

func TestAddServerRefusesMalformedExisting(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	bad := `{"mcpServers": {"other": {"command":"x"}` // truncated, invalid JSON
	if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err == nil {
		t.Fatal("expected error on malformed existing config")
	}
	// The malformed file must be left exactly as-is (not clobbered).
	after, _ := os.ReadFile(p)
	if string(after) != bad {
		t.Fatalf("malformed config was modified; before=%q after=%q", bad, string(after))
	}
}

func TestAddRemoveRefuseNonObjectMcpServers(t *testing.T) {
	for _, body := range []string{`{"mcpServers":[]}`, `{"mcpServers":"x"}`} {
		t.Run(body, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), ".claude.json")
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err == nil {
				t.Fatal("AddServer should refuse a non-object mcpServers")
			}
			if after, _ := os.ReadFile(p); string(after) != body {
				t.Fatalf("AddServer modified file; before=%q after=%q", body, string(after))
			}
			if _, err := os.Stat(p + ".bak"); !os.IsNotExist(err) {
				t.Fatal("AddServer must not leave a .bak behind")
			}

			if err := RemoveServer(p, "clio"); err == nil {
				t.Fatal("RemoveServer should refuse a non-object mcpServers")
			}
			if after, _ := os.ReadFile(p); string(after) != body {
				t.Fatalf("RemoveServer modified file; before=%q after=%q", body, string(after))
			}
			if _, err := os.Stat(p + ".bak"); !os.IsNotExist(err) {
				t.Fatal("RemoveServer must not leave a .bak behind")
			}

			if _, err := HasServer(p, "clio"); err == nil {
				t.Fatal("HasServer should return an error for a non-object mcpServers")
			}
		})
	}
}

func TestAddServerTreatsNullMcpServersAsAbsent(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(p, []byte(`{"mcpServers":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err != nil {
		t.Fatalf("AddServer should treat null mcpServers as absent: %v", err)
	}
	s := servers(t, read(t, p))
	if _, ok := s["clio"]; !ok {
		t.Fatalf("clio entry not written into a fresh object; got %+v", s)
	}
}

func TestHasServer(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	if has, _ := HasServer(p, "clio"); has {
		t.Fatal("missing file should report no server")
	}
	AddServer(p, "clio", ServerEntry{Command: "clio"})
	if has, _ := HasServer(p, "clio"); !has {
		t.Fatal("should report server present")
	}
}

// TestAddServerTopLevelNull guards against a panic when the entire config file is
// the literal JSON `null` (unmarshals to a nil map). It must be treated as an empty
// config, not crash with "assignment to entry in nil map".
func TestAddServerTopLevelNull(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(p, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err != nil {
		t.Fatalf("top-level null config should be treated as empty, got: %v", err)
	}
	s := servers(t, read(t, p))
	if _, ok := s["clio"]; !ok {
		t.Fatal("clio entry not written")
	}
}

package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// setupInstallMCPHome creates a fresh $HOME with an (empty) ~/.claude/projects
// dir — enough for install-mcp's phase-1 ingest to find zero files and succeed
// — and points XDG_DATA_HOME at a separate temp dir so clio's own sqlite index
// doesn't collide with anything under $HOME. Returns the ~/.claude.json path.
func setupInstallMCPHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return filepath.Join(home, ".claude.json")
}

func runInstallMCP(t *testing.T) error {
	t.Helper()
	cmd := newInstallMCPCmd()
	cmd.SetArgs(nil)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.Execute()
}

func readClaudeJSON(t *testing.T, path string) map[string]any {
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

// Case 1: existing MCP servers and unrelated top-level keys survive untouched;
// only a new "clio" entry is added.
func TestInstallMCPPreservesExistingConfig(t *testing.T) {
	cfgPath := setupInstallMCPHome(t)
	orig := `{"mcpServers":{"other":{"command":"other-bin","args":["serve"]}},"theme":"dark"}`
	if err := os.WriteFile(cfgPath, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runInstallMCP(t); err != nil {
		t.Fatalf("install-mcp failed: %v", err)
	}

	root := readClaudeJSON(t, cfgPath)
	if root["theme"] != "dark" {
		t.Error("unrelated top-level key 'theme' was lost")
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers missing or not an object")
	}
	other, ok := servers["other"].(map[string]any)
	if !ok || other["command"] != "other-bin" {
		t.Fatalf("existing 'other' server was lost or altered: %+v", servers["other"])
	}
	clio, ok := servers["clio"].(map[string]any)
	if !ok {
		t.Fatal("clio server entry not added")
	}
	if args, _ := clio["args"].([]any); len(args) != 1 || args[0] != "mcp" {
		t.Fatalf("clio server entry has unexpected args: %+v", clio["args"])
	}
}

// Case 2: running install-mcp twice must not duplicate or corrupt the clio entry.
func TestInstallMCPIdempotent(t *testing.T) {
	cfgPath := setupInstallMCPHome(t)

	if err := runInstallMCP(t); err != nil {
		t.Fatalf("first install-mcp failed: %v", err)
	}
	if err := runInstallMCP(t); err != nil {
		t.Fatalf("second install-mcp failed: %v", err)
	}

	servers, ok := readClaudeJSON(t, cfgPath)["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers missing")
	}
	if len(servers) != 1 {
		t.Fatalf("expected exactly 1 server entry after double install, got %d: %+v", len(servers), servers)
	}
	if _, ok := servers["clio"]; !ok {
		t.Fatal("clio entry missing after double install")
	}
}

// Case 3: pins the persistent-backup behavior (2026-07): a successful
// install-mcp run that actually changes an existing ~/.claude.json leaves a
// <file>.bak the user can manually restore from — claudeconfig.mutateLocked
// only removed the backup on success under the old, transient-safety-net
// design; it now persists after the atomic rename succeeds.
func TestInstallMCPBackupPersistsAfterSuccess(t *testing.T) {
	cfgPath := setupInstallMCPHome(t)
	orig := `{"mcpServers":{"other":{"command":"other-bin"}}}`
	if err := os.WriteFile(cfgPath, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runInstallMCP(t); err != nil {
		t.Fatalf("install-mcp failed: %v", err)
	}

	got, err := os.ReadFile(cfgPath + ".bak")
	if err != nil {
		t.Fatalf("expected a persistent .bak after a content-changing install-mcp run, got: %v", err)
	}
	if string(got) != orig {
		t.Fatalf(".bak content = %q, want pre-install content %q", got, orig)
	}
}

// Case 4: if ~/.claude.json's directory can't be written to (temp file / backup
// creation fails), the original file must be left byte-for-byte intact — no
// partial or truncated JSON.
func TestInstallMCPWriteFailureLeavesOriginalIntact(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	cfgPath := setupInstallMCPHome(t)
	orig := `{"mcpServers":{"other":{"command":"other-bin"}}}`
	if err := os.WriteFile(cfgPath, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(cfgPath)
	if err := os.Chmod(home, 0o500); err != nil { // read+execute only, no write
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(home, 0o700) }) // let t.TempDir() clean up

	err := runInstallMCP(t)
	if err == nil {
		t.Fatal("expected install-mcp to fail when its config dir is not writable")
	}

	after, rerr := os.ReadFile(cfgPath)
	if rerr != nil {
		t.Fatalf("original config file disappeared: %v", rerr)
	}
	if string(after) != orig {
		t.Fatalf("original config was modified on write failure; before=%q after=%q", orig, after)
	}
}

// Case 6: a malformed (invalid JSON) existing ~/.claude.json must be refused,
// not silently overwritten. Pinning this at the CLI-command level (the
// underlying claudeconfig.AddServer behavior is already covered by
// TestAddServerRefusesMalformedExisting in claudeconfig_test.go).
func TestInstallMCPRefusesMalformedExistingConfig(t *testing.T) {
	cfgPath := setupInstallMCPHome(t)
	bad := `{"mcpServers": {"other": {"command":"x"}` // truncated, invalid JSON
	if err := os.WriteFile(cfgPath, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runInstallMCP(t); err == nil {
		t.Fatal("expected install-mcp to error out on a malformed existing config")
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != bad {
		t.Fatalf("malformed config was modified instead of being left alone; before=%q after=%q", bad, after)
	}
}

// uninstall-mcp: mirror check that it removes only clio's own entry.
func TestUninstallMCPRemovesOnlyClio(t *testing.T) {
	cfgPath := setupInstallMCPHome(t)
	orig := `{"mcpServers":{"other":{"command":"other-bin"},"clio":{"command":"clio","args":["mcp"]}},"theme":"dark"}`
	if err := os.WriteFile(cfgPath, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newUninstallMCPCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("uninstall-mcp failed: %v", err)
	}

	root := readClaudeJSON(t, cfgPath)
	if root["theme"] != "dark" {
		t.Error("unrelated top-level key 'theme' was lost")
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if _, ok := servers["clio"]; ok {
		t.Fatal("clio entry not removed")
	}
	if _, ok := servers["other"]; !ok {
		t.Fatal("unrelated 'other' server was wrongly removed")
	}
}

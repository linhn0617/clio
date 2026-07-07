package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// setupInstallHookHome points $HOME at a fresh temp dir, pre-creates
// ~/.claude (mutateLocked's atomic write needs the parent dir to already
// exist — it does not MkdirAll), and returns the ~/.claude/settings.json path
// that install-hook/uninstall-hook write to.
func setupInstallHookHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return filepath.Join(home, ".claude", "settings.json")
}

// stubClioExecutable overrides osExecutable (internal/cli/common.go) so the
// SessionStart command clio writes is a stable, recognizable "clio" path
// instead of the go test binary's own path. Without this, install-hook's own
// isClioRecall() basename check (internal/claudeconfig/hooks.go) never
// matches a test binary, and idempotency can't be exercised end-to-end.
func stubClioExecutable(t *testing.T) {
	t.Helper()
	orig := osExecutable
	osExecutable = func() (string, error) { return "/fake/bin/clio", nil }
	t.Cleanup(func() { osExecutable = orig })
}

func runInstallHook(t *testing.T) error {
	t.Helper()
	cmd := newInstallHookCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.Execute()
}

func readSettingsJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("settings not valid JSON: %v", err)
	}
	return m
}

func hookCommands(t *testing.T, root map[string]any) []string {
	t.Helper()
	hooks, _ := root["hooks"].(map[string]any)
	ss, _ := hooks["SessionStart"].([]any)
	var cmds []string
	for _, g := range ss {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if c, ok := hm["command"].(string); ok {
				cmds = append(cmds, c)
			}
		}
	}
	return cmds
}

// Case 5 (mirrors case 1): existing hooks and unrelated top-level keys survive
// untouched; only clio's recall hook is added.
func TestInstallHookPreservesExistingConfig(t *testing.T) {
	stubClioExecutable(t)
	settingsPath := setupInstallHookHome(t)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	orig := `{"theme":"dark","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/opt/security-scan.sh"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runInstallHook(t); err != nil {
		t.Fatalf("install-hook failed: %v", err)
	}

	root := readSettingsJSON(t, settingsPath)
	if root["theme"] != "dark" {
		t.Error("unrelated top-level key 'theme' was lost")
	}
	cmds := hookCommands(t, root)
	foundExisting, foundClio := false, false
	for _, c := range cmds {
		if c == "/opt/security-scan.sh" {
			foundExisting = true
		}
		if c == "/fake/bin/clio recall" {
			foundClio = true
		}
	}
	if !foundExisting {
		t.Errorf("existing security-scan hook was lost; hooks=%v", cmds)
	}
	if !foundClio {
		t.Errorf("clio recall hook was not added; hooks=%v", cmds)
	}
}

// Case 5 (mirrors case 2): running install-hook twice must not duplicate the
// clio recall hook. Requires stubClioExecutable so both runs produce the same
// recognizable command string (see its doc comment).
func TestInstallHookIdempotent(t *testing.T) {
	stubClioExecutable(t)
	settingsPath := setupInstallHookHome(t)

	if err := runInstallHook(t); err != nil {
		t.Fatalf("first install-hook failed: %v", err)
	}
	if err := runInstallHook(t); err != nil {
		t.Fatalf("second install-hook failed: %v", err)
	}

	cmds := hookCommands(t, readSettingsJSON(t, settingsPath))
	count := 0
	for _, c := range cmds {
		if c == "/fake/bin/clio recall" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 clio recall hook after double install, got %d: %v", count, cmds)
	}
}

// Case 5 (mirrors install_mcp_test.go's TestBackupPersistsAfterSuccess): the
// shared mutateLocked backup now persists after a successful, content-changing
// write (2026-07 behavior change), pinned here for install-hook's
// settings.json path too.
func TestInstallHookBackupPersistsAfterSuccess(t *testing.T) {
	stubClioExecutable(t)
	settingsPath := setupInstallHookHome(t)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	orig := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/opt/security-scan.sh"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runInstallHook(t); err != nil {
		t.Fatalf("install-hook failed: %v", err)
	}

	got, err := os.ReadFile(settingsPath + ".bak")
	if err != nil {
		t.Fatalf("expected a persistent .bak after a content-changing install-hook run, got: %v", err)
	}
	if string(got) != orig {
		t.Fatalf(".bak content = %q, want pre-install content %q", got, orig)
	}
}

// Mirrors install_mcp_test.go's TestInstallMCPWriteFailureLeavesOriginalIntact:
// when the settings.json directory is not writable, install-hook must error
// out and leave the original file's bytes untouched. Also asserts (per the
// claudeconfig backup-only-after-success behavior pinned by
// claudeconfig_test.go's TestBackupNotWrittenOnRenameFailure) that a failed
// write does not leave behind a misleading .bak.
func TestInstallHookWriteFailureLeavesOriginalIntact(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	stubClioExecutable(t)
	settingsPath := setupInstallHookHome(t)
	orig := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/opt/security-scan.sh"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Dir(settingsPath)
	if err := os.Chmod(claudeDir, 0o500); err != nil { // read+execute only, no write
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(claudeDir, 0o700) }) // let t.TempDir() clean up

	err := runInstallHook(t)
	if err == nil {
		t.Fatal("expected install-hook to fail when its config dir is not writable")
	}

	after, rerr := os.ReadFile(settingsPath)
	if rerr != nil {
		t.Fatalf("original settings file disappeared: %v", rerr)
	}
	if string(after) != orig {
		t.Fatalf("original settings was modified on write failure; before=%q after=%q", orig, after)
	}
	if _, err := os.Stat(settingsPath + ".bak"); !os.IsNotExist(err) {
		t.Fatal("a failed write must not create a .bak")
	}
}

// Case 6 (mirrors the install-mcp malformed-config case): a malformed existing
// ~/.claude/settings.json must be refused, not silently overwritten.
func TestInstallHookRefusesMalformedExistingConfig(t *testing.T) {
	stubClioExecutable(t)
	settingsPath := setupInstallHookHome(t)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	bad := `{"hooks": {"SessionStart": [{"hooks":[{"type":"command"` // truncated, invalid JSON
	if err := os.WriteFile(settingsPath, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runInstallHook(t); err == nil {
		t.Fatal("expected install-hook to error out on a malformed existing settings file")
	}

	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != bad {
		t.Fatalf("malformed settings file was modified instead of being left alone; before=%q after=%q", bad, after)
	}
}

// uninstall-hook: mirror check that it removes only clio's own hook.
func TestUninstallHookRemovesOnlyClio(t *testing.T) {
	settingsPath := setupInstallHookHome(t)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	orig := `{"theme":"dark","hooks":{"SessionStart":[
		{"hooks":[{"type":"command","command":"/opt/security-scan.sh"}]},
		{"hooks":[{"type":"command","command":"/fake/bin/clio recall"}]}
	]}}`
	if err := os.WriteFile(settingsPath, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newUninstallHookCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("uninstall-hook failed: %v", err)
	}

	root := readSettingsJSON(t, settingsPath)
	if root["theme"] != "dark" {
		t.Error("unrelated top-level key 'theme' was lost")
	}
	cmds := hookCommands(t, root)
	for _, c := range cmds {
		if c == "/fake/bin/clio recall" {
			t.Fatal("clio recall hook not removed")
		}
	}
	found := false
	for _, c := range cmds {
		if c == "/opt/security-scan.sh" {
			found = true
		}
	}
	if !found {
		t.Error("unrelated security-scan hook was wrongly removed")
	}
}

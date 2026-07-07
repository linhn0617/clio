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

// TestBackupPersistsAfterSuccessfulChange pins the current, intended behavior
// (2026-07): a successful mutation that actually changes an existing file's
// content leaves a persistent <file>.bak the user can manually restore from —
// it is written after the atomic rename succeeds and is not cleaned up. This
// replaced the old "backup is a transient safety net, always removed" design.
func TestBackupPersistsAfterSuccessfulChange(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	orig := []byte(`{"mcpServers":{}}`)
	if err := os.WriteFile(p, orig, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err != nil {
		t.Fatal(err)
	}
	bak := p + ".bak"
	got, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("expected a persistent .bak after a content-changing success, got: %v", err)
	}
	if string(got) != string(orig) {
		t.Fatalf(".bak content = %q, want original content %q", got, orig)
	}
	info, err := os.Stat(bak)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf(".bak mode = %v, want to match original file's mode 0640", info.Mode().Perm())
	}
}

// TestBackupNotUpdatedOnNoopRerun guards against an idempotent rerun silently
// clobbering the one persisted backup: a no-op call (content unchanged) must
// leave a pre-existing .bak completely alone, otherwise "previous version" and
// "current version" collapse into the same content and the backup is useless.
func TestBackupNotUpdatedOnNoopRerun(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	orig := []byte(`{"mcpServers":{}}`)
	if err := os.WriteFile(p, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err != nil {
		t.Fatal(err)
	}
	bak := p + ".bak"
	firstBackup, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("expected .bak after first (content-changing) call: %v", err)
	}

	// Second call is a no-op: "clio" is already present with the same entry.
	if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err != nil {
		t.Fatal(err)
	}
	secondBackup, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf(".bak disappeared after a no-op rerun: %v", err)
	}
	if string(secondBackup) != string(firstBackup) {
		t.Fatalf("no-op rerun changed .bak content; before=%q after=%q", firstBackup, secondBackup)
	}
	if string(secondBackup) != string(orig) {
		t.Fatalf(".bak must still hold the pre-first-change original; got %q, want %q", secondBackup, orig)
	}
}

// TestNoBackupWhenOriginalDidNotExist covers the "first-ever install" case:
// there is no prior version to back up, so no .bak is created.
func TestNoBackupWhenOriginalDidNotExist(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".bak"); !os.IsNotExist(err) {
		t.Fatal("no .bak should be created when the original file did not exist")
	}
}

// TestBackupNotWrittenOnRenameFailure ensures a failed write neither leaves a
// misleading .bak (content identical to the still-unchanged current file) nor
// disturbs any legitimate .bak a prior successful run already persisted: the
// backup is only ever written after the atomic rename has succeeded.
func TestBackupNotWrittenOnRenameFailure(t *testing.T) {
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
		t.Fatal("a failed write must not create a .bak")
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(orig) {
		t.Fatalf("original config changed; before=%q after=%q", orig, after)
	}
}

// TestPriorBackupUnaffectedByLaterFailedWrite covers the second half of the
// failure-path requirement: a .bak persisted by an earlier successful,
// content-changing run must survive untouched when a later mutation on the
// same file fails at the rename step.
func TestPriorBackupUnaffectedByLaterFailedWrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	orig := []byte(`{"mcpServers":{}}`)
	if err := os.WriteFile(p, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	// First call: succeeds and changes content, so it persists a .bak.
	if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err != nil {
		t.Fatal(err)
	}
	bak := p + ".bak"
	before, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("expected .bak after first call: %v", err)
	}

	// Second call: a content-changing mutation (adds a different server) that
	// then fails at the rename step.
	renameFile = func(string, string) error { return fmt.Errorf("forced rename failure") }
	t.Cleanup(func() { renameFile = os.Rename })
	if err := AddServer(p, "other", ServerEntry{Command: "other-bin"}); err == nil {
		t.Fatal("expected error from forced rename failure")
	}

	after, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("prior .bak was removed by a later failed write: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("prior .bak content changed by a later failed write; before=%q after=%q", before, after)
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

// TestPreExistingBakPreservedWhenNoBackupWritten guards against deleting a user's
// unrelated <config>.bak: when configPath is absent this call writes no backup, so
// the cleanup must not remove a pre-existing .bak it did not create.
func TestPreExistingBakPreservedWhenNoBackupWritten(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude.json")
	bak := p + ".bak"
	if err := os.WriteFile(bak, []byte("unrelated backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddServer(p, "clio", ServerEntry{Command: "clio"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("pre-existing unrelated .bak was deleted: %v", err)
	}
	if string(got) != "unrelated backup" {
		t.Fatalf("pre-existing .bak changed: %q", got)
	}
}

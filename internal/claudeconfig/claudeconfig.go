// Package claudeconfig safely edits ~/.claude.json's mcpServers section using a
// read-modify-atomic-write cycle with a backup, so a crash or malformed write
// can never corrupt the user's main Claude Code config. A successful,
// content-changing mutation also leaves a persistent <file>.bak of the
// pre-mutation content so the user can manually recover the previous version.
package claudeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/linhn0617/clio/internal/lock"
)

var renameFile = os.Rename // overridable in tests

// ServerEntry is one mcpServers entry.
type ServerEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// AddServer inserts or updates the named server entry. Existing servers and all
// other config keys are preserved.
func AddServer(configPath, name string, entry ServerEntry) error {
	return mutate(configPath, func(root map[string]any) error {
		servers, err := serversMap(root)
		if err != nil {
			return err
		}
		b, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("marshal server entry: %w", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("unmarshal server entry: %w", err)
		}
		servers[name] = m
		root["mcpServers"] = servers
		return nil
	})
}

// RemoveServer deletes the named server entry if present.
func RemoveServer(configPath, name string) error {
	return mutate(configPath, func(root map[string]any) error {
		servers, err := serversMap(root)
		if err != nil {
			return err
		}
		delete(servers, name)
		root["mcpServers"] = servers
		return nil
	})
}

// HasServer reports whether the named server entry exists.
func HasServer(configPath, name string) (bool, error) {
	root, err := load(configPath)
	if err != nil {
		return false, err
	}
	servers, err := serversMap(root)
	if err != nil {
		return false, err
	}
	_, ok := servers[name]
	return ok, nil
}

func serversMap(root map[string]any) (map[string]any, error) {
	v, ok := root["mcpServers"]
	if !ok || v == nil { // absent or JSON null → safe to create a fresh map
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok { // present, non-null, not an object → meaningful data; refuse
		return nil, fmt.Errorf("mcpServers in config is not a JSON object (found %T); refusing to modify", v)
	}
	return m, nil
}

func load(configPath string) (map[string]any, error) {
	data, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", configPath, err)
	}
	if root == nil { // file was the literal `null` → treat as an empty config
		return map[string]any{}, nil
	}
	return root, nil
}

// mutate serializes the read-modify-write across processes (Unix flock) so concurrent
// install/uninstall runs cannot lost-update each other, then applies mutateLocked.
func mutate(configPath string, fn func(map[string]any) error) error {
	return lock.WithFileLock(configPath, func() error {
		return mutateLocked(configPath, fn)
	})
}

// mutateLocked loads, applies fn, and writes back atomically with a backup.
func mutateLocked(configPath string, fn func(map[string]any) error) error {
	root, err := load(configPath)
	if err != nil {
		return err
	}
	if err := fn(root); err != nil {
		return err
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	// Verify our own output parses before touching anything on disk.
	var verify map[string]any
	if err := json.Unmarshal(out, &verify); err != nil {
		return fmt.Errorf("refusing to write invalid JSON: %w", err)
	}

	// Capture the pre-mutation file (if any) up front. We deliberately do NOT
	// write <config>.bak yet — it is only persisted after the atomic rename
	// below has succeeded, so a failed write can never create, update, or
	// otherwise touch a .bak file (see TestBackupNotWrittenOnRenameFailure /
	// TestPriorBackupUnaffectedByLaterFailedWrite).
	existing, rerr := os.ReadFile(configPath)
	existed := rerr == nil
	origMode := fs.FileMode(0o600)
	if existed {
		if info, serr := os.Stat(configPath); serr == nil {
			origMode = info.Mode().Perm()
		}
	}

	// Atomic write: temp file in the same dir, fsync, rename.
	dir := filepath.Dir(configPath)
	tmp, err := os.CreateTemp(dir, ".claude.json.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := renameFile(tmpName, configPath); err != nil {
		return err
	}
	// fsync the parent dir so the rename survives a crash on filesystems that
	// don't order metadata with data.
	if dirF, derr := os.Open(dir); derr == nil {
		_ = dirF.Sync()
		dirF.Close()
	}

	// Persist a manual-recovery backup of the pre-mutation content, but only
	// when this write actually changed the file. A no-op rerun (idempotent
	// install) must not clobber an existing .bak with content identical to the
	// current file — that would destroy the one prior version the user could
	// still recover. A first-ever install (existed == false) has nothing to
	// back up.
	if existed && !bytes.Equal(existing, out) {
		backup := configPath + ".bak"
		if err := os.WriteFile(backup, existing, origMode); err != nil {
			// debt: the primary write above already succeeded (config is
			// committed) but we still return an error here, so a caller that
			// retries on error could end up re-deriving a different backup
			// baseline. Not handled: acceptable for now since this only
			// triggers on a backup-specific I/O failure (e.g. disk full)
			// right after a successful write, which is rare; revisit if that
			// turns out to happen in practice.
			return fmt.Errorf("write backup: %w", err)
		}
		if err := os.Chmod(backup, origMode); err != nil {
			return fmt.Errorf("chmod backup: %w", err)
		}
	}

	return nil
}

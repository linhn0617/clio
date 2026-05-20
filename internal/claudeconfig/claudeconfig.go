// Package claudeconfig safely edits ~/.claude.json's mcpServers section using a
// read-modify-atomic-write cycle with a backup, so a crash or malformed write
// can never corrupt the user's main Claude Code config.
package claudeconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ServerEntry is one mcpServers entry.
type ServerEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// AddServer inserts or updates the named server entry. Existing servers and all
// other config keys are preserved.
func AddServer(configPath, name string, entry ServerEntry) error {
	return mutate(configPath, func(root map[string]any) error {
		servers := serversMap(root)
		b, _ := json.Marshal(entry)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		servers[name] = m
		root["mcpServers"] = servers
		return nil
	})
}

// RemoveServer deletes the named server entry if present.
func RemoveServer(configPath, name string) error {
	return mutate(configPath, func(root map[string]any) error {
		servers := serversMap(root)
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
	servers := serversMap(root)
	_, ok := servers[name]
	return ok, nil
}

func serversMap(root map[string]any) map[string]any {
	if v, ok := root["mcpServers"].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func load(configPath string) (map[string]any, error) {
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
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
	return root, nil
}

// mutate loads, applies fn, and writes back atomically with a backup.
func mutate(configPath string, fn func(map[string]any) error) error {
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

	// Back up an existing config first.
	backup := configPath + ".bak"
	if existing, rerr := os.ReadFile(configPath); rerr == nil {
		if werr := os.WriteFile(backup, existing, 0o600); werr != nil {
			return fmt.Errorf("write backup: %w", werr)
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
	if err := os.Rename(tmpName, configPath); err != nil {
		return err
	}

	// Success: backup no longer needed.
	os.Remove(backup)
	return nil
}

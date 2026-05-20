// Package config resolves filesystem locations for clio.
package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// ClaudeProjectsDir is where Claude Code stores per-session .jsonl files.
func ClaudeProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// ClaudeConfigFile is Claude Code's main settings file (~/.claude.json).
func ClaudeConfigFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

// DataDir is clio's own state directory. Honors XDG_DATA_HOME, otherwise uses
// the platform default (~/Library/Application Support on macOS, ~/.local/share
// elsewhere).
func DataDir() (string, error) {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "clio"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "clio"), nil
	}
	return filepath.Join(home, ".local", "share", "clio"), nil
}

// DBPath is the SQLite database location.
func DBPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "db.sqlite"), nil
}

// LockPath is the MCP single-writer lock file location.
func LockPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mcp.lock"), nil
}

// EnsureDataDir creates the data directory with 0700 permissions.
func EnsureDataDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

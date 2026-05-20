// Package lock implements a pid-based single-writer lock so the CLI can defer
// to a running MCP server (the sole writer while it runs).
package lock

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Lock represents an acquired lock file.
type Lock struct {
	path string
}

// Acquire writes the current pid to path. It overwrites any stale lock (a lock
// whose recorded process is no longer alive).
func Acquire(path string) (*Lock, error) {
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return nil, err
	}
	return &Lock{path: path}, nil
}

// Release removes the lock file.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	return os.Remove(l.path)
}

// IsHeld reports whether a live process currently holds the lock at path.
// A missing file, unparseable pid, or dead process all count as not held.
func IsHeld(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 probes for existence without actually signaling.
	return proc.Signal(syscall.Signal(0)) == nil
}

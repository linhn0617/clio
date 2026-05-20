// Package lock implements a pid-based single-writer lock so the CLI can defer
// to a running MCP server (the sole writer while it runs).
package lock

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrHeld means another live process already holds the lock.
var ErrHeld = errors.New("lock is held by another running process")

// Lock represents an acquired lock file.
type Lock struct {
	path string
}

// Acquire takes an exclusive lock at path by atomically creating the file
// (O_CREATE|O_EXCL). If the file already exists, it is taken over only when its
// recorded pid is no longer alive (stale lock); otherwise ErrHeld is returned.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		if IsHeld(path) {
			return nil, ErrHeld
		}
		// Stale lock from a dead process: remove and retry once.
		if rmErr := os.Remove(path); rmErr != nil {
			return nil, fmt.Errorf("remove stale lock: %w", rmErr)
		}
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(strconv.Itoa(os.Getpid())); err != nil {
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

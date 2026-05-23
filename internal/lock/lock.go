// Package lock implements a fenced heartbeat leader lease so multiple clio mcp
// processes can coordinate a single watcher with safe, split-brain-free
// failover. The lease file holds "pid nonce unix-seconds". Ownership is fenced
// by the nonce: a superseded leader's Renew/Release become no-ops.
package lock

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrSuperseded means another process took leadership; the caller must demote.
var ErrSuperseded = errors.New("lease superseded by another leader")

// DefaultTTL is how long a heartbeat is considered fresh.
const DefaultTTL = 10 * time.Second

// Lease represents this process's participation in leader election.
type Lease struct {
	path  string
	pid   int
	nonce uint64
	ttl   time.Duration
	now   func() time.Time
	owned bool
}

type record struct {
	pid   int
	nonce uint64
	ts    int64
}

func newLease(path string, ttl time.Duration, now func() time.Time) *Lease {
	return &Lease{path: path, pid: os.Getpid(), ttl: ttl, now: now}
}

// AcquireOrFollow opens the lease at path, becoming leader if it is absent or
// stale, otherwise following. Never fatal.
func AcquireOrFollow(path string) (*Lease, bool, error) {
	l := newLease(path, DefaultTTL, time.Now)
	ok, err := l.TryPromote()
	if err != nil {
		return nil, false, err
	}
	return l, ok, nil
}

// TryPromote takes leadership if the current lease is absent or stale. Returns
// true if this process is now the leader.
func (l *Lease) TryPromote() (bool, error) {
	rec, _ := readRecord(l.path)
	if rec != nil && l.live(rec) && rec.nonce != l.nonce {
		return false, nil
	}
	nonce := rand.Uint64()
	for nonce == 0 {
		nonce = rand.Uint64()
	}
	if err := writeRecordAtomic(l.path, l.pid, nonce, l.now().Unix()); err != nil {
		return false, err
	}
	after, err := readRecord(l.path)
	if err != nil || after == nil || after.nonce != nonce {
		l.owned = false
		return false, nil
	}
	l.nonce, l.owned = nonce, true
	return true, nil
}

// Renew refreshes the heartbeat, or returns ErrSuperseded if another process
// has taken over (the caller must stop its watcher and demote).
func (l *Lease) Renew() error {
	if !l.owned {
		return ErrSuperseded
	}
	rec, err := readRecord(l.path)
	if err != nil || rec == nil || rec.nonce != l.nonce {
		l.owned = false
		return ErrSuperseded
	}
	return writeRecordAtomic(l.path, l.pid, l.nonce, l.now().Unix())
}

// Release removes the lease only if this process still owns it.
func (l *Lease) Release() error {
	if l == nil || !l.owned {
		return nil
	}
	rec, err := readRecord(l.path)
	if err != nil || rec == nil || rec.nonce != l.nonce {
		l.owned = false
		return nil
	}
	l.owned = false
	return os.Remove(l.path)
}

func (l *Lease) live(rec *record) bool {
	if rec.pid != os.Getpid() && !pidAlive(rec.pid) {
		return false
	}
	return l.now().Unix()-rec.ts <= int64(l.ttl/time.Second)
}

// IsHeld reports whether a live (non-stale) leader currently holds the lease.
// Used by CLI commands to defer to a running MCP writer.
func IsHeld(path string) bool {
	rec, err := readRecord(path)
	if err != nil || rec == nil {
		return false
	}
	if rec.pid != os.Getpid() && !pidAlive(rec.pid) {
		return false
	}
	return time.Now().Unix()-rec.ts <= int64(DefaultTTL/time.Second)
}

func readRecord(path string) (*record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := strings.Fields(strings.TrimSpace(string(data)))
	if len(f) != 3 {
		return nil, nil
	}
	pid, e1 := strconv.Atoi(f[0])
	nonce, e2 := strconv.ParseUint(f[1], 10, 64)
	ts, e3 := strconv.ParseInt(f[2], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || pid <= 0 {
		return nil, nil
	}
	return &record{pid: pid, nonce: nonce, ts: ts}, nil
}

func writeRecordAtomic(path string, pid int, nonce uint64, ts int64) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcp.lock.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := fmt.Fprintf(tmp, "%d %d %d", pid, nonce, ts); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

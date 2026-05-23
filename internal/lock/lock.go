// Package lock implements a fenced heartbeat leader lease so multiple clio mcp
// processes can coordinate a single watcher with safe, split-brain-free
// failover. The lease file holds "pid nonce unix-seconds". Ownership is fenced
// by the nonce: a superseded leader's Renew/Release become no-ops.
package lock

import (
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
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

// mutexStaleAfter is the age fallback for .lk files whose holder pid cannot be
// checked (e.g. pid reuse after a long-dead crash). A live-but-slow holder is
// never revoked; only a dead-pid holder (or an unreadably-old file) is broken.
const mutexStaleAfter = 30 * time.Second

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

// mutexPath returns the sidecar lockfile path for a given lease path.
func mutexPath(leasePath string) string { return leasePath + ".lk" }

// withMutex runs fn while holding a cross-process lock on the lease's sidecar
// .lk file, so lease read-modify-write critical sections never interleave
// between processes. The holder's pid is recorded so a crashed holder's lock is
// reclaimed (pid dead), while a live-but-slow holder is never revoked. The age
// fallback only guards against pid reuse of a long-dead holder.
func withMutex(leasePath string, fn func() error) error {
	lk := mutexPath(leasePath)
	for {
		f, err := os.OpenFile(lk, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d", os.Getpid())
			ferr := fn()
			f.Close()
			os.Remove(lk)
			return ferr
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if mutexHolderDead(lk) {
			os.Remove(lk) // O_EXCL on the next loop iteration arbitrates the winner
			continue
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// mutexHolderDead reports whether the .lk holder has crashed: its recorded pid is
// not alive, or — defensively against pid reuse — the lock is older than
// mutexStaleAfter. A live holder mid-critical-section is never reported dead.
func mutexHolderDead(lk string) bool {
	fi, err := os.Stat(lk)
	if err != nil {
		return false // gone already; let the next O_EXCL create win
	}
	if data, rerr := os.ReadFile(lk); rerr == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid > 0 {
			if pid == os.Getpid() {
				return false
			}
			return !pidAlive(pid)
		}
	}
	// Unwritten/garbage pid (holder crashed between create and write): fall back to age.
	return time.Since(fi.ModTime()) > mutexStaleAfter
}

// randomNonce returns a cryptographically random non-zero uint64.
func randomNonce() uint64 {
	var b [8]byte
	for i := 0; i < 10; i++ {
		if _, err := crand.Read(b[:]); err == nil {
			if n := binary.LittleEndian.Uint64(b[:]); n != 0 {
				return n
			}
		}
	}
	// crypto/rand essentially never fails; fall back to a non-zero unique-ish value.
	n := uint64(os.Getpid())<<32 ^ uint64(time.Now().UnixNano())
	if n == 0 {
		n = 1
	}
	return n
}

// TryPromote takes leadership if the current lease is absent or stale. Returns
// true if this process is now the leader.
func (l *Lease) TryPromote() (bool, error) {
	var promoted bool
	err := withMutex(l.path, func() error {
		rec, _ := readRecord(l.path)
		if rec != nil && l.live(rec) && rec.nonce != l.nonce {
			promoted = false
			return nil
		}
		nonce := randomNonce()
		if err := writeRecordAtomic(l.path, l.pid, nonce, l.now().Unix()); err != nil {
			return err
		}
		after, err := readRecord(l.path)
		if err != nil || after == nil || after.nonce != nonce {
			l.owned = false
			promoted = false
			return nil
		}
		l.nonce, l.owned = nonce, true
		promoted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return promoted, nil
}

// Renew refreshes the heartbeat, or returns ErrSuperseded if another process
// has taken over (the caller must stop its watcher and demote).
func (l *Lease) Renew() error {
	if !l.owned {
		return ErrSuperseded
	}
	return withMutex(l.path, func() error {
		rec, err := readRecord(l.path)
		if err != nil || rec == nil || rec.nonce != l.nonce {
			l.owned = false
			return ErrSuperseded
		}
		return writeRecordAtomic(l.path, l.pid, l.nonce, l.now().Unix())
	})
}

// Release removes the lease only if this process still owns it.
func (l *Lease) Release() error {
	if l == nil || !l.owned {
		return nil
	}
	return withMutex(l.path, func() error {
		rec, err := readRecord(l.path)
		if err != nil || rec == nil || rec.nonce != l.nonce {
			l.owned = false
			return nil
		}
		l.owned = false
		return os.Remove(l.path)
	})
}

func (l *Lease) live(rec *record) bool {
	if rec.pid != os.Getpid() && !pidAlive(rec.pid) {
		return false
	}
	return l.now().Unix()-rec.ts <= int64(l.ttl/time.Second)
}

// IsHeld reports whether a live (non-stale) leader currently holds the lease.
// Used by CLI commands to defer to a running MCP writer.
// This is a lock-free best-effort read; no mutex is used.
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

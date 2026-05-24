package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// pidAlive must treat EPERM (process exists but we may not signal it) as alive, so a
// lease owned by a live process we cannot signal is not wrongly stolen.
func TestPidAliveTreatsEPERMAsAlive(t *testing.T) {
	orig := signalProc
	t.Cleanup(func() { signalProc = orig })

	signalProc = func(*os.Process, os.Signal) error { return syscall.EPERM }
	if !pidAlive(os.Getpid()) {
		t.Error("EPERM should be treated as alive")
	}
	signalProc = func(*os.Process, os.Signal) error { return nil }
	if !pidAlive(os.Getpid()) {
		t.Error("nil signal error should be alive")
	}
	signalProc = func(*os.Process, os.Signal) error { return syscall.ESRCH }
	if pidAlive(os.Getpid()) {
		t.Error("ESRCH (no such process) should be dead")
	}
}

func leaseAt(t *testing.T, path string, now *time.Time) *Lease {
	t.Helper()
	l := newLease(path, 10*time.Second, func() time.Time { return *now })
	return l
}

func TestAcquireThenFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	now := time.Unix(1000, 0)

	a := leaseAt(t, path, &now)
	ok, err := a.TryPromote()
	if err != nil || !ok {
		t.Fatalf("A should become leader: ok=%v err=%v", ok, err)
	}

	b := leaseAt(t, path, &now)
	ok, err = b.TryPromote()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("B should follow a live leader, not promote")
	}
}

func TestRenewKeepsLeadership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	now := time.Unix(1000, 0)
	a := leaseAt(t, path, &now)
	if ok, _ := a.TryPromote(); !ok {
		t.Fatal("A should lead")
	}
	now = now.Add(20 * time.Second)
	if err := a.Renew(); err != nil {
		t.Fatalf("owner renew should succeed: %v", err)
	}
}

func TestStaleHeartbeatAllowsTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	now := time.Unix(1000, 0)
	a := leaseAt(t, path, &now)
	if ok, _ := a.TryPromote(); !ok {
		t.Fatal("A should lead")
	}
	now = now.Add(20 * time.Second)
	b := leaseAt(t, path, &now)
	if ok, err := b.TryPromote(); err != nil || !ok {
		t.Fatalf("B should take over a stale lease: ok=%v err=%v", ok, err)
	}
	if err := a.Renew(); err != ErrSuperseded {
		t.Fatalf("A.Renew after takeover = %v, want ErrSuperseded", err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	// A was superseded, so its Release must not delete B's lease file.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("B's lease file should survive A.Release: %v", err)
	}
}

func TestReleaseByOwnerRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	now := time.Unix(1000, 0)
	a := leaseAt(t, path, &now)
	if ok, _ := a.TryPromote(); !ok {
		t.Fatal("A should lead")
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if IsHeld(path) {
		t.Fatal("lease should be released")
	}
}

func TestDeadPidAllowsTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	now := time.Unix(1000, 0)
	// Craft a lease file owned by a dead pid but with a FRESH heartbeat, so only
	// the pid-liveness check (not TTL) can declare it stale.
	deadPid := 2147483646 // almost certainly not a live process
	content := fmt.Sprintf("%d %d %d", deadPid, uint64(12345), now.Unix())
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	b := leaseAt(t, path, &now)
	ok, err := b.TryPromote()
	if err != nil || !ok {
		t.Fatalf("a dead-pid lease must be takeable even with a fresh heartbeat: ok=%v err=%v", ok, err)
	}
}

func TestConcurrentPromoteNoCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	// Use real time so IsHeld (which calls time.Now() internally) sees a fresh
	// heartbeat and can confirm the winner still holds the lease.
	now := time.Now()
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := newLease(path, 10*time.Second, func() time.Time { return now })
			_, _ = l.TryPromote()
		}()
	}
	wg.Wait()
	// Whatever the interleaving, the lease file must be a single valid record
	// (atomic rename never leaves a corrupt/partial file) and a live leader must
	// be held.
	rec, err := readRecord(path)
	if err != nil || rec == nil {
		t.Fatalf("lease file must be a single valid record after concurrent promotes: rec=%v err=%v", rec, err)
	}
	if !IsHeld(path) {
		t.Fatal("a leader should hold the lease after concurrent promotes")
	}
}

func TestTwoPromotersExactlyOneWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	// Shared frozen time so no TTL-based stale decisions interfere.
	now := time.Now()
	const n = 8
	type result struct {
		won bool
		err error
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := newLease(path, 10*time.Second, func() time.Time { return now })
			won, err := l.TryPromote()
			results <- result{won, err}
		}()
	}
	wg.Wait()
	close(results)

	var winners, failures int
	for r := range results {
		if r.err != nil {
			t.Errorf("unexpected error from TryPromote: %v", r.err)
			failures++
		}
		if r.won {
			winners++
		}
	}
	if failures > 0 {
		t.Fatalf("%d goroutine(s) returned errors", failures)
	}
	if winners != 1 {
		t.Fatalf("exactly 1 goroutine should win promotion, got %d", winners)
	}
	if !IsHeld(path) {
		t.Fatal("lease should be held by the winner after concurrent promotes")
	}
}

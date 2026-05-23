package lock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

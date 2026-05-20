package lock

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestAcquireAndIsHeld(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.lock")
	if IsHeld(p) {
		t.Fatal("missing lock should not be held")
	}
	lk, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	if !IsHeld(p) {
		t.Fatal("lock should be held after acquire (own pid)")
	}
	if err := lk.Release(); err != nil {
		t.Fatal(err)
	}
	if IsHeld(p) {
		t.Fatal("lock should not be held after release")
	}
}

func TestAcquireFailsWhenHeldByLiveProcess(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.lock")
	lk, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	// Second acquire by the same live process must be refused.
	if _, err := Acquire(p); err != ErrHeld {
		t.Fatalf("expected ErrHeld, got %v", err)
	}
}

func TestAcquireTakesOverStaleLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.lock")
	// Write a pid that is almost certainly dead.
	if err := os.WriteFile(p, []byte("999999"), 0o600); err != nil {
		t.Fatal(err)
	}
	if IsHeld(p) {
		t.Skip("pid 999999 unexpectedly alive on this host")
	}
	lk, err := Acquire(p)
	if err != nil {
		t.Fatalf("should take over stale lock, got %v", err)
	}
	defer lk.Release()
	data, _ := os.ReadFile(p)
	if got, _ := strconv.Atoi(string(data)); got != os.Getpid() {
		t.Fatalf("lock file should hold our pid, got %s", data)
	}
}

func TestIsHeldUnparseable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.lock")
	os.WriteFile(p, []byte("not-a-pid"), 0o600)
	if IsHeld(p) {
		t.Fatal("unparseable pid should not count as held")
	}
}

func TestReleaseNilSafe(t *testing.T) {
	var lk *Lock
	if err := lk.Release(); err != nil {
		t.Fatalf("nil Release should be safe, got %v", err)
	}
}

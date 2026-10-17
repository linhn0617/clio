package lock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInspectAbsentMalformedFreshStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.lock")
	now := time.Unix(1_000_000, 0)

	if st, err := inspect(path, func() time.Time { return now }); err != nil || st.Present {
		t.Fatalf("absent lease: want Present=false, got %+v err=%v", st, err)
	}

	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err := inspect(path, func() time.Time { return now }); err != nil || !st.Present || !st.Malformed {
		t.Fatalf("malformed lease: want Present+Malformed, got %+v err=%v", st, err)
	}

	// Own pid is alive, so freshness is decided purely by heartbeat age.
	if err := writeRecordAtomic(path, os.Getpid(), 7, now.Unix()-3); err != nil {
		t.Fatal(err)
	}
	st, err := inspect(path, func() time.Time { return now })
	if err != nil || !st.Present || !st.Fresh || !st.Alive || st.PID != os.Getpid() || st.HeartbeatAge != 3*time.Second {
		t.Fatalf("fresh lease: got %+v err=%v", st, err)
	}

	// Alive but past the TTL: not Fresh, yet Alive (a busy leader, not a dead one).
	if err := writeRecordAtomic(path, os.Getpid(), 7, now.Unix()-300); err != nil {
		t.Fatal(err)
	}
	st, _ = inspect(path, func() time.Time { return now })
	if st.Fresh || !st.Alive || st.HeartbeatAge != 300*time.Second {
		t.Fatalf("late heartbeat from a live pid: want Alive && !Fresh, got %+v", st)
	}
}

func TestInspectDeadPidIsNotAliveEvenWithFreshHeartbeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	now := time.Unix(1_000_000, 0)
	const deadPid = 2147483646 // same convention as TestDeadPidAllowsTakeover
	if err := writeRecordAtomic(path, deadPid, 7, now.Unix()-1); err != nil {
		t.Fatal(err)
	}
	st, err := inspect(path, func() time.Time { return now })
	if err != nil || !st.Present || st.Alive || st.Fresh || st.PID != deadPid {
		t.Fatalf("dead pid: want Present && !Alive && !Fresh, got %+v err=%v", st, err)
	}
}

func TestInspectDoesNotModifyLeaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	if err := writeRecordAtomic(path, os.Getpid(), 7, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := Inspect(path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("Inspect rewrote the lease: %q -> %q", before, after)
	}
}

// IsHeld is defined as Inspect().Fresh so the two can never disagree.
func TestIsHeldAgreesWithInspect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.lock")
	if IsHeld(path) {
		t.Fatal("absent lease must not be held")
	}
	if err := writeRecordAtomic(path, os.Getpid(), 7, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if !IsHeld(path) {
		t.Fatal("fresh lease from a live pid must be held")
	}
	if err := writeRecordAtomic(path, os.Getpid(), 7, time.Now().Unix()-300); err != nil {
		t.Fatal(err)
	}
	if IsHeld(path) {
		t.Fatal("late heartbeat must not be held")
	}
}

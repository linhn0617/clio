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
	if err != nil || !st.Present || !st.Fresh || st.PID != os.Getpid() || st.HeartbeatAge != 3*time.Second {
		t.Fatalf("fresh lease: got %+v err=%v", st, err)
	}

	if err := writeRecordAtomic(path, os.Getpid(), 7, now.Unix()-300); err != nil {
		t.Fatal(err)
	}
	st, _ = inspect(path, func() time.Time { return now })
	if st.Fresh || st.HeartbeatAge != 300*time.Second {
		t.Fatalf("stale lease must not be Fresh: %+v", st)
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

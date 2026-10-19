package lock

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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

// Inspect must not create, rewrite or remove anything next to the lease (no mutex
// sidecar, no temp file), whatever state the lease is in.
func TestInspectDoesNotModifyLeaseDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.lock")
	check := func(state string) {
		t.Helper()
		before := snapshot(t, dir)
		if _, err := Inspect(path); err != nil {
			t.Fatalf("%s: %v", state, err)
		}
		if after := snapshot(t, dir); !reflect.DeepEqual(before, after) {
			t.Fatalf("%s: Inspect changed the directory:\nbefore %v\nafter  %v", state, before, after)
		}
	}
	check("absent")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	check("malformed")
	if err := writeRecordAtomic(path, os.Getpid(), 7, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	check("fresh")
	if err := writeRecordAtomic(path, 2147483646, 7, time.Now().Unix()-300); err != nil {
		t.Fatal(err)
	}
	check("stale")
}

// snapshot records name, mode, size, mtime and sha256 of everything under root.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := de.Info()
		if err != nil {
			return err
		}
		sum := ""
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum = fmt.Sprintf("%x", sha256.Sum256(b))
		}
		out[p] = fmt.Sprintf("%v|%d|%d|%s", fi.Mode(), fi.Size(), fi.ModTime().UnixNano(), sum)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
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

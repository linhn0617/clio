package lock

import (
	"errors"
	"io/fs"
	"os"
	"time"
)

// Status is a read-only snapshot of a lease file, for diagnostics.
type Status struct {
	Present      bool          // the lease file exists
	Malformed    bool          // it exists but does not parse as "pid nonce ts"
	PID          int           // leader pid (valid when Present && !Malformed)
	HeartbeatAge time.Duration // now minus the last heartbeat
	Fresh        bool          // pid alive and heartbeat within DefaultTTL — the same test IsHeld uses
}

// Inspect reads the lease at path without taking the mutex or writing anything.
// IsHeld only answers yes/no; doctor also needs to tell "no mcp running" from
// "a leader died and left a stale lease", and to report the heartbeat age.
func Inspect(path string) (Status, error) { return inspect(path, time.Now) }

func inspect(path string, now func() time.Time) (Status, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return Status{}, nil
	} else if err != nil {
		return Status{}, err
	}
	rec, err := readRecord(path)
	if errors.Is(err, fs.ErrNotExist) { // removed between Stat and read
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	if rec == nil {
		return Status{Present: true, Malformed: true}, nil
	}
	age := time.Duration(now().Unix()-rec.ts) * time.Second
	alive := rec.pid == os.Getpid() || pidAlive(rec.pid)
	return Status{
		Present:      true,
		PID:          rec.pid,
		HeartbeatAge: age,
		Fresh:        alive && age <= DefaultTTL,
	}, nil
}

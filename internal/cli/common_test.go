package cli

import (
	"testing"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/lock"
)

func TestOpenForQueryDefersToLiveLeader(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if _, err := config.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}
	dbPath, err := config.DBPath()
	if err != nil {
		t.Fatal(err)
	}
	// Seed the index so openAndCatchUp doesn't error on "no index".
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seed.Close()

	lockPath, err := config.LockPath()
	if err != nil {
		t.Fatal(err)
	}
	lease, isLeader, err := lock.AcquireOrFollow(lockPath)
	if err != nil || !isLeader {
		t.Fatalf("expected to lead: leader=%v err=%v", isLeader, err)
	}
	defer lease.Release()

	if !lock.IsHeld(lockPath) {
		t.Fatal("lock should read as held while a live leader exists")
	}
	// openAndCatchUp should defer to the leader and return a usable RO handle.
	d, err := openAndCatchUp()
	if err != nil {
		t.Fatalf("openAndCatchUp: %v", err)
	}
	defer d.Close()
}

package cli

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
)

func TestLeaderLoopDemotesOnSupersede(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if _, err := config.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}
	lockPath, err := config.LockPath()
	if err != nil {
		t.Fatal(err)
	}

	lease, isLeader, err := lock.AcquireOrFollow(lockPath)
	if err != nil || !isLeader {
		t.Fatalf("expected leader: isLeader=%v err=%v", isLeader, err)
	}
	defer lease.Release()

	// Run leaderLoop in the background. projects="" so it does no watcher/ingest,
	// just the renew ticker.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- leaderLoop(ctx, lease, ingest.New(nil, nil), "", discardLogger()) }()

	// Simulate another process taking over: overwrite the lease file with a
	// different nonce + fresh timestamp. The next Renew (<= ~3s) must see the
	// nonce mismatch and demote.
	// Lease file format is "pid nonce unixSeconds".
	overwrite := fmt.Sprintf("%d %d %d", os.Getpid()+1, uint64(999999), time.Now().Unix())
	if err := os.WriteFile(lockPath, []byte(overwrite), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case demoted := <-done:
		if !demoted {
			t.Fatal("leaderLoop returned false (ctx cancel), expected true (demote)")
		}
	case <-time.After(2*leaderRenewInterval + time.Second):
		t.Fatal("leaderLoop did not demote within two renew intervals")
	}
}

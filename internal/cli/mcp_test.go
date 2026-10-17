package cli

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestLeaderLoopDemotesOnSupersede(t *testing.T) {
	lease, lockPath := leaseFixture(t)

	// projects="" so leaderLoop does no catch-up or watcher, just the renewer.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	var flag atomic.Bool
	env := testEnv(&fakeIngester{}, leaderRenewInterval)
	go func() { done <- leaderLoop(ctx, lease, &flag, env, "", discardLogger(), false) }()

	// Another process takes over; the next Renew (<= ~3s) must see the nonce
	// mismatch and demote.
	supersede(t, lockPath)

	select {
	case demoted := <-done:
		if !demoted {
			t.Fatal("leaderLoop returned false (ctx cancel), expected true (demote)")
		}
	case <-time.After(2*leaderRenewInterval + time.Second):
		t.Fatal("leaderLoop did not demote within two renew intervals")
	}
}

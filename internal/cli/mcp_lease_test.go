package cli

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
)

// fakeIngester stands in for the Ingester in leaderLoop/beforeRead tests.
// IngestAll blocks for slow (or until ctx is done) and records its calls.
type fakeIngester struct {
	slow       time.Duration
	ingestAll  atomic.Int32
	running    atomic.Int32 // concurrent IngestAll calls right now
	maxRunning atomic.Int32
	ctxErr     atomic.Value // error seen when IngestAll returned early
	started    chan struct{}
}

func (f *fakeIngester) IngestAll(ctx context.Context, _ string, _ bool) (ingest.Stats, error) {
	f.ingestAll.Add(1)
	n := f.running.Add(1)
	defer f.running.Add(-1)
	for {
		m := f.maxRunning.Load()
		if n <= m || f.maxRunning.CompareAndSwap(m, n) {
			break
		}
	}
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	select {
	case <-time.After(f.slow):
		return ingest.Stats{}, nil
	case <-ctx.Done():
		f.ctxErr.Store(ctx.Err())
		return ingest.Stats{}, ctx.Err()
	}
}
func (f *fakeIngester) BackfillActivity(context.Context) error     { return nil }
func (f *fakeIngester) PurgeMissing(context.Context, string) error { return nil }

func leaseFixture(t *testing.T) (*lock.Lease, string) {
	t.Helper()
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
	t.Cleanup(func() { lease.Release() })
	return lease, lockPath
}

func testEnv(ing leaderIngester, renew time.Duration) *leaderEnv {
	return &leaderEnv{ing: ing, ingestMu: &sync.Mutex{}, renewEvery: renew,
		startWatcher: func(ctx context.Context) { <-ctx.Done() }}
}

func heartbeat(lockPath string) string {
	b, _ := os.ReadFile(lockPath)
	return string(b)
}

// (a) The lease is renewed while the startup catch-up is still running, and the
// leader flag stays false until it ends.
func TestLeaderLoopRenewsDuringCatchup(t *testing.T) {
	lease, lockPath := leaseFixture(t)
	fake := &fakeIngester{slow: 2300 * time.Millisecond, started: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var flag atomic.Bool
	done := make(chan bool, 1)
	go func() {
		done <- leaderLoop(ctx, lease, &flag, testEnv(fake, 100*time.Millisecond), "/p", discardLogger())
	}()

	<-fake.started
	seen := map[string]bool{heartbeat(lockPath): true}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		seen[heartbeat(lockPath)] = true
		if flag.Load() {
			t.Fatal("leader flag set before catch-up finished")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(seen) < 2 {
		t.Fatalf("lease heartbeat not renewed during catch-up; distinct records seen: %d", len(seen))
	}
	cancel()
	if <-done {
		t.Fatal("ctx cancel should return false")
	}
}

// (b) Superseded during catch-up cancels the catch-up and demotes.
func TestLeaderLoopSupersededDuringCatchupCancelsIt(t *testing.T) {
	lease, lockPath := leaseFixture(t)
	fake := &fakeIngester{slow: time.Minute, started: make(chan struct{}, 1)}
	var flag atomic.Bool
	done := make(chan bool, 1)
	go func() {
		done <- leaderLoop(context.Background(), lease, &flag, testEnv(fake, 50*time.Millisecond), "/p", discardLogger())
	}()

	<-fake.started
	overwrite := fmt.Sprintf("%d %d %d", os.Getpid()+1, uint64(999999), time.Now().Unix())
	if err := os.WriteFile(lockPath, []byte(overwrite), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case demoted := <-done:
		if !demoted {
			t.Fatal("expected true (demote)")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("catch-up was not cancelled after supersede")
	}
	if fake.ctxErr.Load() == nil {
		t.Fatal("IngestAll ctx was not cancelled")
	}
	if flag.Load() {
		t.Fatal("leader flag must stay false after a cancelled catch-up")
	}
}

// (c) The leader runs the catch-up exactly once.
func TestLeaderLoopCatchupRunsOnce(t *testing.T) {
	lease, _ := leaseFixture(t)
	fake := &fakeIngester{}
	ctx, cancel := context.WithCancel(context.Background())
	var flag atomic.Bool
	done := make(chan bool, 1)
	go func() {
		done <- leaderLoop(ctx, lease, &flag, testEnv(fake, 50*time.Millisecond), "/p", discardLogger())
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !flag.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !flag.Load() {
		t.Fatal("leader flag never set")
	}
	cancel()
	<-done
	if n := fake.ingestAll.Load(); n != 1 {
		t.Fatalf("IngestAll calls = %d, want 1", n)
	}
}

// (d) An early read during the leader's catch-up (flag still false) is served
// at once, without a second concurrent walk on the Ingester.
func TestBeforeReadDuringLeaderCatchupDoesNotWalkConcurrently(t *testing.T) {
	lease, _ := leaseFixture(t)
	fake := &fakeIngester{slow: 800 * time.Millisecond, started: make(chan struct{}, 1)}
	env := testEnv(fake, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var flag atomic.Bool
	done := make(chan bool, 1)
	go func() { done <- leaderLoop(ctx, lease, &flag, env, "/p", discardLogger()) }()
	<-fake.started

	br := newBeforeRead(ctx, env, &flag, "/p", discardLogger())
	start := time.Now()
	br()
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("beforeRead blocked %v behind the leader catch-up", d)
	}
	if flag.Load() {
		t.Fatal("precondition: leader flag should still be false")
	}
	if m := fake.maxRunning.Load(); m != 1 {
		t.Fatalf("max concurrent IngestAll = %d, want 1", m)
	}
	if n := fake.ingestAll.Load(); n != 1 {
		t.Fatalf("IngestAll calls = %d, want 1 (read must skip)", n)
	}
	cancel()
	<-done
}

// A follower (no catch-up in flight) still gets its throttled read catch-up,
// and a confirmed leader never does.
func TestBeforeReadFollowerCatchesUp(t *testing.T) {
	fake := &fakeIngester{}
	env := testEnv(fake, time.Second)
	var flag atomic.Bool
	br := newBeforeRead(context.Background(), env, &flag, "/p", discardLogger())
	br()
	br() // within 1s: throttled
	if n := fake.ingestAll.Load(); n != 1 {
		t.Fatalf("IngestAll calls = %d, want 1", n)
	}
	flag.Store(true)
	time.Sleep(1100 * time.Millisecond)
	br()
	if n := fake.ingestAll.Load(); n != 1 {
		t.Fatalf("leader beforeRead walked: calls = %d", n)
	}
}

package cli

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
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
	ignoreCtx  bool // return nil after ctx is done, like an ingest that swallows cancellation
	backfill   atomic.Int32
	purge      atomic.Int32
	stats      ingest.Stats // returned by a completed IngestAll
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
		return f.stats, nil
	case <-ctx.Done():
		f.ctxErr.Store(ctx.Err())
		if f.ignoreCtx {
			return ingest.Stats{}, nil
		}
		return ingest.Stats{}, ctx.Err()
	}
}
func (f *fakeIngester) BackfillActivity(context.Context) error {
	f.backfill.Add(1)
	return nil
}
func (f *fakeIngester) PurgeMissing(context.Context, string) error {
	f.purge.Add(1)
	return nil
}

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

// supersede simulates another process taking the lease: it rewrites the lease file
// under the lock package's file lock so it cannot interleave with a Renew.
func supersede(t *testing.T, lockPath string) {
	t.Helper()
	overwrite := fmt.Sprintf("%d %d %d", os.Getpid()+1, uint64(999999), time.Now().Unix())
	err := lock.WithFileLock(lockPath, func() error { return os.WriteFile(lockPath, []byte(overwrite), 0o600) })
	if err != nil {
		t.Fatal(err)
	}
}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
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
		done <- leaderLoop(ctx, lease, &flag, testEnv(fake, 100*time.Millisecond), "/p", discardLogger(), false)
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
		done <- leaderLoop(context.Background(), lease, &flag, testEnv(fake, 50*time.Millisecond), "/p", discardLogger(), false)
	}()

	<-fake.started
	supersede(t, lockPath)
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
		done <- leaderLoop(ctx, lease, &flag, testEnv(fake, 50*time.Millisecond), "/p", discardLogger(), false)
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
	go func() { done <- leaderLoop(ctx, lease, &flag, env, "/p", discardLogger(), false) }()
	<-fake.started

	br := newBeforeRead(ctx, env, &flag, "/p", discardLogger())
	start := time.Now()
	br(context.Background())
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
	br(context.Background())
	br(context.Background()) // within 1s: throttled
	if n := fake.ingestAll.Load(); n != 1 {
		t.Fatalf("IngestAll calls = %d, want 1", n)
	}
	flag.Store(true)
	time.Sleep(1100 * time.Millisecond)
	br(context.Background())
	if n := fake.ingestAll.Load(); n != 1 {
		t.Fatalf("leader beforeRead walked: calls = %d", n)
	}
}

// Item 1: a process that starts as leader holds the ingest mutex before it can
// serve, so a read that arrives straight after startup cannot win the lock and run
// a cold rebuild of its own. The leader flag is still false at that point.
func TestStartLeaseRoleEarlyReadCannotRunColdRebuild(t *testing.T) {
	lease, _ := leaseFixture(t)
	fake := &fakeIngester{slow: 700 * time.Millisecond, started: make(chan struct{}, 1)}
	env := testEnv(fake, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	var flag atomic.Bool
	wait := startLeaseRole(ctx, lease, true, &flag, env, "/p", discardLogger())

	br := newBeforeRead(ctx, env, &flag, "/p", discardLogger())
	br(context.Background()) // arrives before the role goroutine has even started
	<-fake.started
	br(context.Background())
	if flag.Load() {
		t.Fatal("precondition: flag must still be false during catch-up")
	}
	// Let the leader's catch-up finish: had an early read won the lock it would
	// have walked first and the leader would walk again afterwards.
	deadline := time.Now().Add(3 * time.Second)
	for !flag.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !flag.Load() {
		t.Fatal("leader never finished its catch-up")
	}
	if n := fake.ingestAll.Load(); n != 1 {
		t.Fatalf("IngestAll calls = %d, want 1 (only the leader's catch-up)", n)
	}
	cancel()
	wait()
}

// Item 1 (recheck): flag turning true between the first check and TryLock must
// not start a walk.
func TestBeforeReadRechecksLeaderFlagUnderLock(t *testing.T) {
	fake := &fakeIngester{}
	env := testEnv(fake, time.Second)
	var flag atomic.Bool
	br := newBeforeRead(context.Background(), env, &flag, "/p", discardLogger())
	env.ingestMu.Lock()
	go func() {
		time.Sleep(100 * time.Millisecond)
		flag.Store(true) // leader publishes true while holding the mutex
		env.ingestMu.Unlock()
	}()
	// Spin until the lock is free and the flag is true; no call may ever walk.
	for i := 0; i < 50; i++ {
		br(context.Background())
		time.Sleep(5 * time.Millisecond)
	}
	if n := fake.ingestAll.Load(); n != 0 {
		t.Fatalf("beforeRead walked %d times around leader publication", n)
	}
}

// Item 2: shutdown order as RunE does it: cancel, join the role goroutine (catch-up,
// watcher, renewer), only then release the lease. After the join nothing may still
// be touching the ingester or the watcher.
func TestShutdownJoinsCatchupAndWatcher(t *testing.T) {
	lease, _ := leaseFixture(t)
	fake := &fakeIngester{slow: time.Minute, started: make(chan struct{}, 1)}
	env := testEnv(fake, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	var flag atomic.Bool
	wait := startLeaseRole(ctx, lease, true, &flag, env, "/p", discardLogger())
	<-fake.started
	cancel()
	wait()
	if fake.running.Load() != 0 {
		t.Fatal("IngestAll still running after join")
	}
	lease.Release()

	// With a finished catch-up the watcher is running; join must wait for it too.
	lease2, _ := leaseFixture(t)
	var watcherDone atomic.Bool
	env2 := testEnv(&fakeIngester{}, 50*time.Millisecond)
	env2.startWatcher = func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(100 * time.Millisecond)
		watcherDone.Store(true)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	var flag2 atomic.Bool
	wait2 := startLeaseRole(ctx2, lease2, true, &flag2, env2, "/p", discardLogger())
	flagDeadline := time.Now().Add(3 * time.Second)
	for !flag2.Load() && time.Now().Before(flagDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !flag2.Load() {
		t.Fatal("leader never finished its catch-up")
	}
	time.Sleep(50 * time.Millisecond) // let the watcher goroutine start
	cancel2()
	wait2()
	if !watcherDone.Load() {
		t.Fatal("join returned before the watcher exited")
	}
}

// Item 4: once the catch-up is cancelled by a supersede, no later phase runs and
// the cancellation is logged as info, not as a failure.
func TestSupersededCatchupStopsBeforeLaterPhases(t *testing.T) {
	lease, lockPath := leaseFixture(t)
	fake := &fakeIngester{slow: time.Minute, ignoreCtx: true, started: make(chan struct{}, 1)}
	buf := &syncBuf{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	var flag atomic.Bool
	done := make(chan bool, 1)
	go func() {
		done <- leaderLoop(context.Background(), lease, &flag, testEnv(fake, 50*time.Millisecond), "/p", log, false)
	}()
	<-fake.started
	supersede(t, lockPath)
	select {
	case demoted := <-done:
		if !demoted {
			t.Fatal("expected demote")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no demotion")
	}
	if b, p := fake.backfill.Load(), fake.purge.Load(); b != 0 || p != 0 {
		t.Fatalf("later phases ran after cancel: backfill=%d purge=%d", b, p)
	}
	out := buf.String()
	if !strings.Contains(out, "catch-up cancelled") || !strings.Contains(out, "superseded") {
		t.Fatalf("missing info log about cancelled catch-up:\n%s", out)
	}
	if strings.Contains(out, "failed") {
		t.Fatalf("cancellation logged as failure:\n%s", out)
	}
}

// Item 3: the watcher's backstop guard takes the shared mutex without blocking.
func TestWalkGuardSharesIngestMutex(t *testing.T) {
	env := testEnv(&fakeIngester{}, time.Second)
	release := env.tryWalk()
	if release == nil {
		t.Fatal("free mutex must be granted")
	}
	if env.tryWalk() != nil {
		t.Fatal("second guard must be refused while one walk runs")
	}
	release()
	if r := env.tryWalk(); r == nil {
		t.Fatal("mutex not released")
	} else {
		r()
	}
}

// Item 5: the whole command, with the Serve seam returning as if stdin hit EOF,
// runs the startup catch-up exactly once (nothing before Serve ingests as well).
func TestMCPCommandStartupIngestsOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &fakeIngester{}
	origRT, origServe := newMCPRuntime, serveMCP
	t.Cleanup(func() { newMCPRuntime, serveMCP = origRT, origServe })
	newMCPRuntime = func(_ *db.DB, _ string, _ *slog.Logger, _ func() func()) mcpRuntime {
		return mcpRuntime{ing: fake, watch: func(ctx context.Context) { <-ctx.Done() }}
	}
	serveMCP = func(*server.MCPServer) error {
		// Stand-in for stdin EOF: give a stray second ingest time to show up.
		time.Sleep(500 * time.Millisecond)
		return nil
	}
	cmd := newMCPCmd()
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if n := fake.ingestAll.Load(); n != 1 {
		t.Fatalf("IngestAll calls during startup = %d, want 1", n)
	}
}

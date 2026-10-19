package cli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/watcher"
)

// The read catch-up walk is bounded by the request's ctx (mcp-go waits for
// in-flight handlers after stdin closes) as well as by the command's ctx.
func TestBeforeReadReturnsWhenRequestCtxCancelled(t *testing.T) {
	fake := &fakeIngester{slow: time.Minute, started: make(chan struct{}, 1)}
	env := testEnv(fake, time.Second)
	var flag atomic.Bool
	buf := &syncBuf{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	br := newBeforeRead(context.Background(), env, &flag, "/p", log)

	reqCtx, cancelReq := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() { br(reqCtx); close(returned) }()
	<-fake.started
	cancelReq()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("beforeRead still walking 100ms after the request ctx was cancelled")
	}
	// A walk cut short by cancellation is not a failure.
	if strings.Contains(buf.String(), "failed") {
		t.Fatalf("cancelled read catch-up logged as failure:\n%s", buf.String())
	}
	if env.tryWalk() == nil {
		t.Fatal("ingestMu not released after cancelled walk")
	}
}

func TestBeforeReadReturnsWhenCommandCtxCancelled(t *testing.T) {
	fake := &fakeIngester{slow: time.Minute, started: make(chan struct{}, 1)}
	env := testEnv(fake, time.Second)
	var flag atomic.Bool
	cmdCtx, cancelCmd := context.WithCancel(context.Background())
	br := newBeforeRead(cmdCtx, env, &flag, "/p", discardLogger())
	returned := make(chan struct{})
	go func() { br(context.Background()); close(returned) }()
	<-fake.started
	cancelCmd()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("beforeRead still walking after the command ctx was cancelled")
	}
}

// shutdownFake: IngestAll blocks until ctx is done, then keeps "working" for lag
// (like an ingest that only notices cancellation between files) and records
// whether the lease file still existed when it finished.
type shutdownFake struct {
	lag          time.Duration
	lockPath     string
	started      chan struct{}
	finished     atomic.Bool
	lockAtFinish atomic.Bool
}

func (f *shutdownFake) IngestAll(ctx context.Context, _ string, _ bool) (ingest.Stats, error) {
	select {
	case f.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	time.Sleep(f.lag)
	_, err := os.Stat(f.lockPath)
	f.lockAtFinish.Store(err == nil)
	f.finished.Store(true)
	return ingest.Stats{}, ctx.Err()
}
func (f *shutdownFake) BackfillActivity(context.Context) error     { return nil }
func (f *shutdownFake) PurgeMissing(context.Context, string) error { return nil }

func cmdHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := config.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}
}

// RunE must cancel and join the role goroutine before it lets the deferred lease
// release and DB close run.
func TestMCPCommandShutdownJoinsBeforeRelease(t *testing.T) {
	cmdHome(t)
	lockPath, _ := config.LockPath()
	fake := &shutdownFake{lag: 300 * time.Millisecond, lockPath: lockPath, started: make(chan struct{}, 1)}
	origRT, origServe := newMCPRuntime, serveMCP
	t.Cleanup(func() { newMCPRuntime, serveMCP = origRT, origServe })
	newMCPRuntime = func(_ *db.DB, _ string, _ *slog.Logger, _ func() func()) mcpRuntime {
		return mcpRuntime{ing: fake, watch: func(ctx context.Context) { <-ctx.Done() }}
	}
	serveMCP = func(*server.MCPServer) error {
		<-fake.started
		return nil // stdin EOF while the catch-up is in flight
	}
	cmd := newMCPCmd()
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if !fake.finished.Load() {
		t.Fatal("RunE returned while the catch-up was still running (no join before Release/Close)")
	}
	if !fake.lockAtFinish.Load() {
		t.Fatal("lease was released before the catch-up finished")
	}
	if _, err := os.Stat(lockPath); err == nil {
		t.Fatal("lease file still present after RunE returned")
	}
}

// The watcher newMCPRuntime builds really gets the walk guard.
func TestNewMCPRuntimeWiresBackstopGuard(t *testing.T) {
	cmdHome(t)
	origRun := runWatcher
	t.Cleanup(func() { runWatcher = origRun })
	var got *watcher.Watcher
	runWatcher = func(_ context.Context, w *watcher.Watcher) error { got = w; return nil }

	env := testEnv(&fakeIngester{}, time.Second)
	rt := newMCPRuntime(nil, "/p", discardLogger(), env.tryWalk)
	rt.watch(context.Background())
	if got == nil || got.BackstopGuard == nil {
		t.Fatal("watcher built without a BackstopGuard")
	}
	env.ingestMu.Lock()
	if got.BackstopGuard() != nil {
		t.Fatal("guard granted a walk while ingestMu was held")
	}
	env.ingestMu.Unlock()
	if r := got.BackstopGuard(); r == nil {
		t.Fatal("guard refused a free mutex")
	} else {
		r()
	}
}

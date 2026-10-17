package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
	"github.com/linhn0617/clio/internal/mcp"
	"github.com/linhn0617/clio/internal/watcher"
)

const (
	followerPollInterval = 5 * time.Second
	leaderRenewInterval  = 3 * time.Second
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the stdio MCP server for Claude Code",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := stderrLogger()

			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			// RW handle: migrations + all writes (watcher + catch-up).
			rw, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer rw.Close()
			// RO handle: serves read tools; never blocks on the write lock.
			ro, err := db.OpenReadOnly(dbPath)
			if err != nil {
				return err
			}
			defer ro.Close()

			lockPath, err := config.LockPath()
			if err != nil {
				return err
			}
			lease, isLeader, err := lock.AcquireOrFollow(lockPath)
			if err != nil {
				return fmt.Errorf("acquire lease: %w", err)
			}
			defer lease.Release()

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			projects, _ := config.ClaudeProjectsDir()

			// Always false until the leader's catch-up has finished (and published
			// true under ingestMu): a process that merely holds the lease is not yet
			// fresh, so early reads go through beforeRead.
			var isLeaderNow atomic.Bool

			// No synchronous startup ingest here: Serve must answer immediately, and the
			// leader's single startup catch-up runs inside leaderLoop (with the lease
			// already being renewed).
			env := &leaderEnv{ingestMu: &sync.Mutex{}, renewEvery: leaderRenewInterval}
			rt := newMCPRuntime(rw, projects, log, env.tryWalk)
			env.ing, env.startWatcher = rt.ing, rt.watch
			beforeRead := newBeforeRead(ctx, env, &isLeaderNow, projects, log)

			waitRole := startLeaseRole(ctx, lease, isLeader, &isLeaderNow, env, projects, log)

			log.Warn("clio mcp server starting", "leader", isLeader)
			srv := mcp.NewServer(ro, version, beforeRead)
			err = serveMCP(srv)
			// Stop catch-up, watcher and renewer before the lease is released and the
			// DB handles close (deferred above): they all use both.
			cancel()
			waitRole()
			return err
		},
	}
}

// serveMCP is a seam so the command-level startup test can end Serve at will.
var serveMCP = mcp.Serve

// mcpRuntime is what the command builds its ingestion side from: the ingester the
// leader lifecycle drives, and the blocking watcher run.
type mcpRuntime struct {
	ing   leaderIngester
	watch func(ctx context.Context)
}

// runWatcher is a seam so tests can capture the watcher newMCPRuntime builds.
var runWatcher = func(ctx context.Context, w *watcher.Watcher) error { return w.Run(ctx) }

// newMCPRuntime is a seam for tests; guard is handed to the watcher's backstop.
var newMCPRuntime = func(rw *db.DB, projects string, log *slog.Logger, guard func() func()) mcpRuntime {
	ing := ingest.NewWithBuiltinSources(rw, log)
	return mcpRuntime{ing: ing, watch: func(ctx context.Context) {
		w := watcher.New(ing, projects, log)
		w.BackstopGuard = guard
		if err := runWatcher(ctx, w); err != nil {
			log.Warn("watcher stopped", "err", err)
		}
	}}
}

// leaderIngester is the slice of *ingest.Ingester the leader lifecycle uses; an
// interface so tests can inject a slow or counting catch-up.
type leaderIngester interface {
	IngestAll(ctx context.Context, projectsDir string, force bool) (ingest.Stats, error)
	BackfillActivity(ctx context.Context) error
	PurgeMissing(ctx context.Context, projectsDir string) error
}

// leaderEnv bundles what leaderLoop and beforeRead share.
type leaderEnv struct {
	ing leaderIngester
	// ingestMu serializes full walks on the one Ingester: the leader's startup
	// catch-up holds it (Lock) for its whole duration, beforeRead and the watcher
	// backstop only TryLock and skip when it is taken. Waiting instead would block a
	// read for as long as a full rebuild takes.
	ingestMu   *sync.Mutex
	renewEvery time.Duration
	// startWatcher blocks until ctx is done.
	startWatcher func(ctx context.Context)
}

// tryWalk takes ingestMu without blocking. It returns the release func, or nil if
// another walk holds it.
func (e *leaderEnv) tryWalk() func() {
	if !e.ingestMu.TryLock() {
		return nil
	}
	return e.ingestMu.Unlock
}

// newBeforeRead builds the best-effort, throttled read catch-up. It is a no-op
// for a confirmed leader (the watcher keeps the DB fresh). A process that holds
// the lease but is still in its startup catch-up is not yet flagged leader, so
// early reads land here; ingestMu keeps them off the leader's in-flight walk.
// The walk stops when either the request's ctx or cmdCtx is cancelled: mcp-go
// waits for in-flight handlers after stdin closes, so a cold-index walk bound
// only to the command ctx would hold shutdown hostage.
func newBeforeRead(cmdCtx context.Context, env *leaderEnv, leaderFlag *atomic.Bool, projects string, log *slog.Logger) func(context.Context) {
	var mu sync.Mutex
	var lastCatchup time.Time
	return func(reqCtx context.Context) {
		if projects == "" || leaderFlag.Load() {
			return
		}
		mu.Lock()
		throttled := time.Since(lastCatchup) < time.Second
		mu.Unlock()
		if throttled {
			return
		}
		release := env.tryWalk()
		if release == nil {
			return // another walk (leader startup / earlier read) is running
		}
		defer release()
		// The leader publishes the flag while holding ingestMu, so a flag that turned
		// true while we were acquiring means the walk is already done.
		if leaderFlag.Load() {
			return
		}
		ctx, cancel := context.WithCancel(reqCtx)
		defer cancel()
		defer context.AfterFunc(cmdCtx, cancel)()
		_, err := env.ing.IngestAll(ctx, projects, false)

		mu.Lock()
		lastCatchup = time.Now()
		mu.Unlock()
		if err != nil && ctx.Err() == nil {
			log.Warn("read catch-up failed (serving possibly-stale)", "err", err)
		}
	}
}

// startLeaseRole launches runLeaseRole and returns a func that blocks until it (and
// everything it started: catch-up, watcher, renewer) has exited. A starting leader
// takes ingestMu here, synchronously, before the caller can Serve, so no early read
// can win the lock and run a cold rebuild of its own; leaderLoop inherits and
// releases it.
func startLeaseRole(ctx context.Context, lease *lock.Lease, isLeader bool, leaderFlag *atomic.Bool, env *leaderEnv, projects string, log *slog.Logger) (wait func()) {
	// debt: 三個機制（預先持鎖跨 goroutine、TryLock、flag 重查）可由單一三態 role 原子（starting/leader/follower）取代；若再有呼叫者需要 leaderLoop 就要換。
	preHeld := isLeader && projects != ""
	if preHeld {
		env.ingestMu.Lock()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runLeaseRole(ctx, lease, isLeader, preHeld, leaderFlag, env, projects, log)
	}()
	return func() { <-done }
}

// runLeaseRole drives leader/follower transitions until ctx is cancelled.
// leaderFlag is kept current so beforeRead can skip catch-up when not needed.
// leaderFlag.Store(true) happens in leaderLoop, after the startup catch-up
// completes, so a just-promoted leader never serves stale reads.
// preHeld says the caller already took env.ingestMu for the first leaderLoop.
func runLeaseRole(ctx context.Context, lease *lock.Lease, isLeader, preHeld bool, leaderFlag *atomic.Bool, env *leaderEnv, projects string, log *slog.Logger) {
	for {
		if !isLeader {
			leaderFlag.Store(false)
			if !pollUntilLeader(ctx, lease, log) {
				return // ctx done
			}
		}
		// Returns true if superseded (demote), false if ctx done.
		demoted := leaderLoop(ctx, lease, leaderFlag, env, projects, log, preHeld)
		preHeld = false
		if !demoted {
			return
		}
		// Demoted: clear the flag before going back to follower polling.
		leaderFlag.Store(false)
		isLeader = false
	}
}

// pollUntilLeader polls TryPromote every 5s. Returns true once promoted, false
// if ctx is cancelled.
func pollUntilLeader(ctx context.Context, lease *lock.Lease, log *slog.Logger) bool {
	t := time.NewTicker(followerPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			ok, err := lease.TryPromote()
			if err != nil {
				log.Warn("promote attempt failed", "err", err)
				continue
			}
			if ok {
				return true
			}
		}
	}
}

// leaderLoop runs lease renewal, the single startup catch-up, then the watcher.
// Returns true if the lease was superseded (caller demotes), false if ctx was
// cancelled. Renewal starts before the catch-up and spans the whole call: a
// catch-up (a full rebuild can take minutes) longer than the lease TTL would
// otherwise let another clio take over. If renewal reports ErrSuperseded mid
// catch-up, the catch-up is cancelled and we demote at once. Everything started
// here (renewer, watcher) has exited when it returns.
// preHeld means the caller already holds env.ingestMu; the catch-up releases it.
// debt: 三個機制（預先持鎖跨 goroutine、TryLock、flag 重查）可由單一三態 role 原子（starting/leader/follower）取代；若再有呼叫者需要 leaderLoop 就要換。
// leaderFlag is flipped to true only after the catch-up completes, while still
// holding ingestMu, so a just-promoted follower never serves stale reads and no
// early read can start a walk of its own in between.
func leaderLoop(ctx context.Context, lease *lock.Lease, leaderFlag *atomic.Bool, env *leaderEnv, projects string, log *slog.Logger, preHeld bool) bool {
	wctx, wcancel := context.WithCancelCause(ctx)

	var wg sync.WaitGroup
	// Wait for renewer and watcher before returning: the caller reuses lease
	// (TryPromote) right after a demotion and Lease is not safe for concurrent use.
	defer func() {
		wcancel(context.Canceled)
		wg.Wait()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		renew := time.NewTicker(env.renewEvery)
		defer renew.Stop()
		for {
			select {
			case <-wctx.Done():
				return
			case <-renew.C:
				if err := lease.Renew(); err != nil {
					if errors.Is(err, lock.ErrSuperseded) {
						log.Warn("lease superseded; demoting to follower")
						wcancel(lock.ErrSuperseded) // also aborts an in-flight catch-up
						return
					}
					log.Warn("lease renew failed", "err", err)
				}
			}
		}
	}()

	// Run the startup catch-up + watcher regardless of whether the Claude projects dir
	// exists: on a Codex-only machine it is absent, but the Codex source still has
	// history to ingest and live-watch. IngestAll/PurgeMissing/watcher all handle a
	// missing root per-source.
	if projects != "" {
		if !startupCatchup(wctx, leaderFlag, env, projects, log, preHeld) {
			return superseded(wctx)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			env.startWatcher(wctx)
		}()
	} else {
		// No projects path resolved; nothing to catch up.
		leaderFlag.Store(true)
	}

	<-wctx.Done()
	return superseded(wctx)
}

// superseded reports whether wctx was cancelled by the renewer (as opposed to the
// parent ctx ending).
func superseded(wctx context.Context) bool {
	return errors.Is(context.Cause(wctx), lock.ErrSuperseded)
}

// startupCatchup runs ingest, activity backfill and purge-missing under ingestMu and
// publishes leaderFlag=true before releasing it. Returns false if cancelled (the
// flag is left false); the ctx is checked after every phase so a superseded leader
// stops at once.
func startupCatchup(wctx context.Context, leaderFlag *atomic.Bool, env *leaderEnv, projects string, log *slog.Logger, preHeld bool) bool {
	if !preHeld {
		env.ingestMu.Lock()
	}
	defer env.ingestMu.Unlock()

	cancelled := func() bool {
		if wctx.Err() == nil {
			return false
		}
		if superseded(wctx) {
			log.Warn("catch-up cancelled: superseded")
		} else {
			log.Info("catch-up cancelled: shutting down")
		}
		return true
	}
	if _, err := env.ing.IngestAll(wctx, projects, false); err != nil && wctx.Err() == nil {
		log.Warn("startup catch-up failed", "err", err)
	}
	if cancelled() {
		return false
	}
	if err := env.ing.BackfillActivity(wctx); err != nil && wctx.Err() == nil {
		log.Warn("activity backfill failed", "err", err)
	}
	if cancelled() {
		return false
	}
	// Reconcile deletions that happened while clio was down before we start
	// serving: deleted transcripts are marked retained (kept, not purged), and
	// files that came back are unmarked.
	if err := env.ing.PurgeMissing(wctx, projects); err != nil && wctx.Err() == nil {
		log.Warn("startup purge failed", "err", err)
	}
	if cancelled() {
		return false
	}
	// Catch-up done: safe to serve reads without follower catch-up.
	leaderFlag.Store(true)
	return true
}

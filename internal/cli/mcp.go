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
			ing := ingest.NewWithBuiltinSources(rw, log)

			var isLeaderNow atomic.Bool
			isLeaderNow.Store(isLeader)

			// No synchronous startup ingest here: Serve must answer immediately, and the
			// leader's single startup catch-up runs inside leaderLoop (with the lease
			// already being renewed). Until it finishes isLeaderNow stays false, so
			// beforeRead's throttled catch-up covers reads that arrive early.
			env := &leaderEnv{
				ing:        ing,
				ingestMu:   &sync.Mutex{},
				renewEvery: leaderRenewInterval,
				startWatcher: func(wctx context.Context) {
					if err := watcher.New(ing, projects, log).Run(wctx); err != nil {
						log.Warn("watcher stopped", "err", err)
					}
				},
			}
			beforeRead := newBeforeRead(ctx, env, &isLeaderNow, projects, log)

			go runLeaseRole(ctx, lease, isLeader, &isLeaderNow, env, projects, log)

			log.Warn("clio mcp server starting", "leader", isLeader)
			srv := mcp.NewServer(ro, version, beforeRead)
			return mcp.Serve(srv)
		},
	}
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
	// ingestMu serializes catch-up walks on the one Ingester: the leader's startup
	// catch-up holds it (Lock) for its whole duration, beforeRead only TryLocks and
	// skips (serving possibly-stale data) when it is taken. Waiting instead would
	// block a read for as long as a full rebuild takes.
	ingestMu   *sync.Mutex
	renewEvery time.Duration
	// startWatcher blocks until ctx is done.
	startWatcher func(ctx context.Context)
}

// newBeforeRead builds the best-effort, throttled read catch-up. It is a no-op
// for a confirmed leader (the watcher keeps the DB fresh). A process that holds
// the lease but is still in its startup catch-up is not yet flagged leader, so
// early reads land here; ingestMu keeps them off the leader's in-flight walk.
func newBeforeRead(ctx context.Context, env *leaderEnv, leaderFlag *atomic.Bool, projects string, log *slog.Logger) func() {
	var mu sync.Mutex
	var lastCatchup time.Time
	return func() {
		if projects == "" || leaderFlag.Load() {
			return
		}
		mu.Lock()
		throttled := time.Since(lastCatchup) < time.Second
		mu.Unlock()
		if throttled || !env.ingestMu.TryLock() {
			return // throttled, or another walk (leader startup / earlier read) is running
		}
		_, err := env.ing.IngestAll(ctx, projects, false)
		env.ingestMu.Unlock()

		mu.Lock()
		lastCatchup = time.Now()
		mu.Unlock()
		if err != nil {
			log.Warn("read catch-up failed (serving possibly-stale)", "err", err)
		}
	}
}

// runLeaseRole drives leader/follower transitions until ctx is cancelled.
// leaderFlag is kept current so beforeRead can skip catch-up when not needed.
// leaderFlag.Store(true) is deferred into leaderLoop, after the startup
// catch-up completes, so a just-promoted leader never serves stale reads.
func runLeaseRole(ctx context.Context, lease *lock.Lease, isLeader bool, leaderFlag *atomic.Bool, env *leaderEnv, projects string, log *slog.Logger) {
	for {
		if !isLeader {
			leaderFlag.Store(false)
			if !pollUntilLeader(ctx, lease, log) {
				return // ctx done
			}
		}
		// leaderFlag.Store(true) is now done inside leaderLoop, after startup catch-up.
		// Returns true if superseded (demote), false if ctx done.
		if !leaderLoop(ctx, lease, leaderFlag, env, projects, log) {
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
// catch-up, the catch-up is cancelled and we demote at once.
// leaderFlag is flipped to true only after the catch-up completes so that a
// just-promoted follower never serves stale reads in the promotion window.
func leaderLoop(ctx context.Context, lease *lock.Lease, leaderFlag *atomic.Bool, env *leaderEnv, projects string, log *slog.Logger) bool {
	wctx, wcancel := context.WithCancel(ctx)

	superseded := make(chan struct{})
	var renewWG sync.WaitGroup
	renewWG.Add(1)
	go func() {
		defer renewWG.Done()
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
						close(superseded)
						wcancel() // abort an in-flight catch-up
						return
					}
					log.Warn("lease renew failed", "err", err)
				}
			}
		}
	}()
	// Wait for the renewer before returning: the caller reuses lease (TryPromote)
	// right after a demotion and Lease is not safe for concurrent use.
	defer func() {
		wcancel()
		renewWG.Wait()
	}()

	// Run the startup catch-up + watcher regardless of whether the Claude projects dir
	// exists: on a Codex-only machine it is absent, but the Codex source still has
	// history to ingest and live-watch. IngestAll/PurgeMissing/watcher all handle a
	// missing root per-source.
	if projects != "" {
		env.ingestMu.Lock()
		if _, err := env.ing.IngestAll(wctx, projects, false); err != nil {
			log.Warn("startup catch-up failed", "err", err)
		}
		if err := env.ing.BackfillActivity(wctx); err != nil {
			log.Warn("activity backfill failed", "err", err)
		}
		// Reconcile deletions that happened while clio was down before we start
		// serving: deleted transcripts are marked retained (kept, not purged), and
		// files that came back are unmarked.
		if err := env.ing.PurgeMissing(wctx, projects); err != nil {
			log.Warn("startup purge failed", "err", err)
		}
		env.ingestMu.Unlock()
		if wctx.Err() != nil {
			// Cancelled mid catch-up: superseded (demote) or shutting down.
			select {
			case <-superseded:
				return true
			default:
				return false
			}
		}
		// Catch-up done: safe to serve reads without follower catch-up.
		leaderFlag.Store(true)
		go env.startWatcher(wctx)
	} else {
		// No projects path resolved; nothing to catch up.
		leaderFlag.Store(true)
	}

	select {
	case <-ctx.Done():
		return false
	case <-superseded:
		return true
	}
}

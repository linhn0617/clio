package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
			ing := ingest.New(rw, log)
			ing.AddCodexSource() // also ingest Codex CLI history, when installed

			var isLeaderNow atomic.Bool
			isLeaderNow.Store(isLeader)

			// Leader: do an initial synchronous ingest so the DB is ready before the
			// first MCP read arrives. The watcher (launched inside runLeaseRole) then
			// keeps it fresh; beforeRead is a no-op for the leader.
			if isLeader && projects != "" {
				if _, err := ing.IngestAll(ctx, projects, false); err != nil {
					log.Warn("startup catch-up failed", "err", err)
				}
			}

			// Best-effort, throttled read catch-up for followers. Skipped entirely
			// when this process is the leader (the watcher keeps the DB fresh).
			// An in-flight guard ensures at most one IngestAll walk runs at a time;
			// concurrent callers return immediately and serve possibly-stale data.
			var catchupMu sync.Mutex
			var lastCatchup time.Time
			var catchupRunning bool
			beforeRead := func() {
				if projects == "" || isLeaderNow.Load() {
					return // leader is kept fresh by the watcher; nothing to do
				}
				catchupMu.Lock()
				if catchupRunning || time.Since(lastCatchup) < time.Second {
					catchupMu.Unlock()
					return
				}
				catchupRunning = true
				catchupMu.Unlock()

				_, err := ing.IngestAll(ctx, projects, false)

				catchupMu.Lock()
				catchupRunning = false
				lastCatchup = time.Now()
				catchupMu.Unlock()
				if err != nil {
					log.Warn("read catch-up failed (serving possibly-stale)", "err", err)
				}
			}

			go runLeaseRole(ctx, lease, isLeader, &isLeaderNow, ing, projects, log)

			log.Warn("clio mcp server starting", "leader", isLeader)
			srv := mcp.NewServer(ro, version, beforeRead)
			return mcp.Serve(srv)
		},
	}
}

// runLeaseRole drives leader/follower transitions until ctx is cancelled.
// leaderFlag is kept current so beforeRead can skip catch-up when not needed.
// leaderFlag.Store(true) is deferred into leaderLoop, after the startup
// catch-up completes, so a just-promoted leader never serves stale reads.
func runLeaseRole(ctx context.Context, lease *lock.Lease, isLeader bool, leaderFlag *atomic.Bool, ing *ingest.Ingester, projects string, log *slog.Logger) {
	for {
		if !isLeader {
			leaderFlag.Store(false)
			if !pollUntilLeader(ctx, lease, log) {
				return // ctx done
			}
		}
		// leaderFlag.Store(true) is now done inside leaderLoop, after startup catch-up.
		// Returns true if superseded (demote), false if ctx done.
		if !leaderLoop(ctx, lease, leaderFlag, ing, projects, log) {
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

// leaderLoop runs the watcher + 3s renew while leader. Returns true if the lease
// was superseded (caller demotes), false if ctx was cancelled.
// leaderFlag is flipped to true only after the startup catch-up completes so
// that a just-promoted follower never serves stale reads in the promotion window.
func leaderLoop(ctx context.Context, lease *lock.Lease, leaderFlag *atomic.Bool, ing *ingest.Ingester, projects string, log *slog.Logger) bool {
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()

	if projects != "" {
		if _, err := os.Stat(projects); err == nil {
			if _, err := ing.IngestAll(wctx, projects, false); err != nil {
				log.Warn("startup catch-up failed", "err", err)
			}
			if err := ing.BackfillActivity(wctx); err != nil {
				log.Warn("activity backfill failed", "err", err)
			}
			// Reflect deletions that happened while clio was down before we start
			// serving, so a just-promoted leader (and CLI readers deferring to it)
			// don't surface sources that no longer exist.
			if err := ing.PurgeMissing(wctx, projects); err != nil {
				log.Warn("startup purge failed", "err", err)
			}
			// Catch-up done: safe to serve reads without follower catch-up.
			leaderFlag.Store(true)
			go func() {
				if err := watcher.New(ing, projects, log).Run(wctx); err != nil {
					log.Warn("watcher stopped", "err", err)
				}
			}()
		} else {
			// projects dir doesn't exist; nothing to catch up.
			leaderFlag.Store(true)
		}
	} else {
		// No projects configured; nothing to catch up.
		leaderFlag.Store(true)
	}

	renew := time.NewTicker(leaderRenewInterval)
	defer renew.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-renew.C:
			if err := lease.Renew(); err != nil {
				if errors.Is(err, lock.ErrSuperseded) {
					log.Warn("lease superseded; demoting to follower")
					return true
				}
				log.Warn("lease renew failed", "err", err)
			}
		}
	}
}

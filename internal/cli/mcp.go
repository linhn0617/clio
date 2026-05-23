package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
	"github.com/linhn0617/clio/internal/mcp"
	"github.com/linhn0617/clio/internal/watcher"
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

			// Best-effort, throttled read catch-up for followers (and harmless for
			// leaders, who are kept fresh by the watcher). Never fails a read.
			var catchupMu sync.Mutex
			var lastCatchup time.Time
			beforeRead := func() {
				if projects == "" {
					return
				}
				catchupMu.Lock()
				if time.Since(lastCatchup) < time.Second {
					catchupMu.Unlock()
					return
				}
				lastCatchup = time.Now()
				catchupMu.Unlock()
				if _, err := ing.IngestAll(projects, false); err != nil {
					log.Warn("read catch-up failed (serving possibly-stale)", "err", err)
				}
			}

			go runLeaseRole(ctx, lease, isLeader, ing, projects, log)

			log.Warn("clio mcp server starting", "leader", isLeader)
			srv := mcp.NewServer(ro, version, beforeRead)
			return mcp.Serve(srv)
		},
	}
}

// runLeaseRole drives leader/follower transitions until ctx is cancelled.
func runLeaseRole(ctx context.Context, lease *lock.Lease, isLeader bool, ing *ingest.Ingester, projects string, log *slog.Logger) {
	for {
		if !isLeader {
			if !pollUntilLeader(ctx, lease, log) {
				return // ctx done
			}
		}
		// Now leader. leaderLoop returns true if superseded (demote), false if ctx done.
		if !leaderLoop(ctx, lease, ing, projects, log) {
			return
		}
		isLeader = false
	}
}

// pollUntilLeader polls TryPromote every 5s. Returns true once promoted, false
// if ctx is cancelled.
func pollUntilLeader(ctx context.Context, lease *lock.Lease, log *slog.Logger) bool {
	t := time.NewTicker(5 * time.Second)
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
func leaderLoop(ctx context.Context, lease *lock.Lease, ing *ingest.Ingester, projects string, log *slog.Logger) bool {
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()

	if projects != "" {
		if _, err := os.Stat(projects); err == nil {
			if _, err := ing.IngestAll(projects, false); err != nil {
				log.Warn("startup catch-up failed", "err", err)
			}
			go func() {
				if err := watcher.New(ing, projects, log).Run(wctx); err != nil {
					log.Warn("watcher stopped", "err", err)
				}
			}()
		}
	}

	renew := time.NewTicker(3 * time.Second)
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

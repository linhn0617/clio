package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

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
			// stdout is reserved for JSON-RPC; everything else goes to stderr.
			log := stderrLogger()

			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer database.Close()

			// Become the sole writer and catch up once before serving.
			if dir, err := config.EnsureDataDir(); err == nil {
				_ = dir
			}
			lockPath, err := config.LockPath()
			if err != nil {
				return err
			}
			lk, err := lock.Acquire(lockPath)
			if err != nil {
				if errors.Is(err, lock.ErrHeld) {
					return fmt.Errorf("another clio mcp server is already running; not starting a second writer")
				}
				return fmt.Errorf("acquire writer lock: %w", err)
			}
			defer lk.Release()

			if projects, perr := config.ClaudeProjectsDir(); perr == nil {
				if _, serr := os.Stat(projects); serr == nil {
					ing := ingest.New(database, log)
					if _, ierr := ing.IngestAll(projects, false); ierr != nil {
						log.Warn("initial catch-up failed", "err", ierr)
					}
					// Live-ingest new activity while we serve.
					ctx, cancel := context.WithCancel(cmd.Context())
					defer cancel()
					go func() {
						if werr := watcher.New(ing, projects, log).Run(ctx); werr != nil {
							log.Warn("watcher stopped", "err", werr)
						}
					}()
				}
			}

			log.Info("clio mcp server starting")
			srv := mcp.NewServer(database, version)
			return mcp.Serve(srv)
		},
	}
}

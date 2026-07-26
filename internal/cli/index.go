package cli

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
	"github.com/linhn0617/clio/internal/sessions"
)

func newIndexCmd() *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Scan and index Claude Code session history",
		RunE: func(cmd *cobra.Command, args []string) error {
			// A running MCP leader keeps the index current, so there is
			// nothing for us to do. It is not strictly the sole writer
			// (follower MCP processes also catch-up ingest before reads);
			// concurrent writers are serialized by WAL + busy_timeout,
			// and ingest is idempotent.
			if lockPath, err := config.LockPath(); err == nil && lock.IsHeld(lockPath) {
				if full {
					// A requested full rebuild (e.g. the usage backfill after an
					// upgrade) must never be silently skipped as success.
					return fmt.Errorf("clio mcp holds the index lock — a full re-index cannot run now; stop Claude Code (or the clio MCP server) and re-run `clio index --full`")
				}
				fmt.Fprintln(os.Stdout, "clio mcp is running and keeping the index current — nothing to do.")
				return nil
			}
			projects, err := config.ClaudeProjectsDir()
			if err != nil {
				return err
			}
			if _, err := os.Stat(projects); err != nil && !nonDefaultSourceAvailable() {
				return bootstrapMissingSourcesError(projects, err)
			}
			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer database.Close()

			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
			ing := ingest.NewWithBuiltinSources(database, log)

			st, err := ing.IngestAll(cmd.Context(), projects, full)
			if err != nil {
				return err
			}
			if err := ing.BackfillActivity(cmd.Context()); err != nil {
				log.Warn("activity backfill failed", "err", err)
			}
			fmt.Fprintf(os.Stdout, "indexed: %d files (%d ingested, %d skipped), %d messages added\n",
				st.FilesScanned, st.FilesIngested, st.FilesSkipped, st.MessagesAdded)
			// A full rebuild is the restore path for `clio prune-raw`. IngestAll
			// logs per-file failures and continues, so a pruned session whose file
			// became unreadable/undiscoverable would stay pruned silently — surface
			// it with a non-zero exit rather than reporting a clean success.
			if full {
				residual, rerr := sessions.PrunedResiduals(cmd.Context(), database)
				if rerr != nil {
					return fmt.Errorf("pruned-residual check failed: %w", rerr)
				}
				if len(residual) > 0 {
					fmt.Fprintf(os.Stderr, "warning: %d session(s) still have pruned raw_json after a full reindex (source file unreadable/undiscoverable?):\n", len(residual))
					for _, u := range residual {
						fmt.Fprintf(os.Stderr, "  %s\n", u)
					}
					return fmt.Errorf("%d pruned session(s) could not be restored", len(residual))
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "Force a full re-index instead of incremental")
	return cmd
}

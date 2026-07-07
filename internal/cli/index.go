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
				fmt.Fprintln(os.Stdout, "clio mcp is running and keeping the index current — nothing to do.")
				return nil
			}
			projects, err := config.ClaudeProjectsDir()
			if err != nil {
				return err
			}
			if _, err := os.Stat(projects); err != nil && !codexAvailable() {
				return fmt.Errorf("no sessions to index: neither %s nor a Codex sessions dir exists: %w", projects, err)
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
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "Force a full re-index instead of incremental")
	return cmd
}

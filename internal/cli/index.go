package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
	"github.com/linhn0617/clio/internal/sessions"
	"github.com/linhn0617/clio/internal/termsafe"
)

func newIndexCmd() *cobra.Command {
	var full, purgeMissing, force bool
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Scan and index Claude Code session history",
		RunE: func(cmd *cobra.Command, args []string) error {
			if force && !purgeMissing {
				return fmt.Errorf("--force only applies to --purge-missing")
			}
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
				if purgeMissing {
					// Same rule as --full: an explicit delete must never be skipped as success.
					return fmt.Errorf("clio mcp holds the index lock — retained sessions cannot be purged now; stop Claude Code (or the clio MCP server) and re-run `clio index --purge-missing`")
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

			st, err := runIndexIngest(cmd.Context(), ing, projects, full, log)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "indexed: %d files (%d ingested, %d skipped), %d messages added\n",
				st.FilesScanned, st.FilesIngested, st.FilesSkipped, st.MessagesAdded)
			if purgeMissing {
				deleteMissing := ing.DeleteMissing
				if force {
					deleteMissing = ing.ForceDeleteMissing
				}
				n, perr := deleteMissing(cmd.Context(), projects)
				if errors.Is(perr, ingest.ErrPurgeRefused) {
					return fmt.Errorf("%w — nothing was deleted", perr)
				}
				if perr != nil {
					return perr
				}
				fmt.Fprintf(os.Stdout, "purged: %d session source(s) whose transcript no longer exists\n", n)
			}
			if full {
				// After --purge-missing so its deletes' tombstones are merged too.
				fmt.Fprintln(os.Stdout, "optimizing fts index…")
				if oerr := optimizeFTS(cmd.Context(), database); oerr != nil {
					log.Warn("fts optimize failed", "err", oerr)
				} else {
					fmt.Fprintln(os.Stdout, "fts optimized")
				}
			}
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
					fmt.Fprintf(os.Stderr, "warning: %d session(s) still have pruned raw_json after a full reindex (source file unreadable/undiscoverable, or deleted — `clio index --purge-missing` drops deleted ones):\n", len(residual))
					for _, u := range residual {
						fmt.Fprintf(os.Stderr, "  %s\n", termsafe.Strip(u))
					}
					return fmt.Errorf("%d pruned session(s) could not be restored", len(residual))
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "Force a full re-index instead of incremental")
	cmd.Flags().BoolVar(&purgeMissing, "purge-missing", false, "Delete indexed sessions whose source transcript no longer exists (kept by default)")
	cmd.Flags().BoolVar(&force, "force", false, "With --purge-missing: delete even when many transcripts went missing together (normally refused as a likely unmounted directory)")
	return cmd
}

// optimizeFTS merges all FTS5 segments into one. A full reindex DELETEs then re-inserts
// every message, and FTS5 deletes only leave tombstones that nothing else ever merges
// away, so without this each rebuild stacks another copy of the index on disk. Callers
// treat failure as non-fatal: the index content is already correct.
func optimizeFTS(ctx context.Context, d *db.DB) error {
	_, err := d.ExecContext(ctx, `INSERT INTO messages_fts(messages_fts) VALUES('optimize')`)
	return err
}

// runIndexIngest always repairs missing targets across all history, ignoring the
// read-path watermark, even when incremental ingestion finds no changed files.
func runIndexIngest(ctx context.Context, ing interface {
	leaderIngester
	RepairActivity(context.Context) error
}, projects string, full bool, log *slog.Logger) (ingest.Stats, error) {
	return runIngestBackfill(ctx, ing, projects, full, false, ing.RepairActivity, log)
}

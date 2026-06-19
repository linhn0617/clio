package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
)

// addSourceFlag registers the shared --source flag (which agent tool's history to
// read), defaulting to Claude Code so existing behavior is unchanged.
func addSourceFlag(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVar(p, "source", "claude-code", "Which tool's history: claude-code | codex | all")
}

// validateSource rejects an unknown --source value before it reaches a query.
func validateSource(s string) error {
	switch s {
	case "", "claude-code", "codex", "all":
		return nil
	default:
		return fmt.Errorf("invalid --source %q (want claude-code, codex, or all)", s)
	}
}

func stderrLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openForQuery opens the database for a read-mostly CLI command and performs a
// quick incremental catch-up so results reflect the latest sessions. When a
// live MCP leader is detected via the lock file, the CLI defers to it and
// opens the database read-only instead of running its own catch-up.
func openForQuery() (*db.DB, error) {
	dbPath, err := config.DBPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no index found at %s — run `clio index` first", dbPath)
	}

	// Defer to a running MCP leader: open read-only and skip catch-up to avoid
	// write contention with the live leader.
	if lockPath, err := config.LockPath(); err == nil && lock.IsHeld(lockPath) {
		return db.OpenReadOnly(dbPath)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		return nil, err
	}
	if projects, err := config.ClaudeProjectsDir(); err == nil {
		if _, statErr := os.Stat(projects); statErr == nil {
			ing := ingest.New(database, discardLogger())
			ing.AddCodexSource() // also catch up Codex CLI history, when installed
			if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
				stderrLogger().Warn("incremental catch-up failed", "err", err)
			}
			if err := ing.PurgeMissing(context.Background(), projects); err != nil {
				stderrLogger().Warn("catch-up purge failed", "err", err)
			}
			if err := ing.BackfillActivity(context.Background()); err != nil {
				stderrLogger().Warn("activity backfill failed", "err", err)
			}
		}
	}
	return database, nil
}

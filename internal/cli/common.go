package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
)

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

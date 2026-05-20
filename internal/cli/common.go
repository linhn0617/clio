package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)

func stderrLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openForQuery opens the database for a read-mostly CLI command and performs a
// quick incremental catch-up so results reflect the latest sessions. (Phase 3
// will make this defer to a running MCP writer via a lock file.)
func openForQuery() (*db.DB, error) {
	dbPath, err := config.DBPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no index found at %s — run `clio index` first", dbPath)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		return nil, err
	}
	if projects, err := config.ClaudeProjectsDir(); err == nil {
		if _, statErr := os.Stat(projects); statErr == nil {
			ing := ingest.New(database, discardLogger())
			if _, err := ing.IngestAll(projects, false); err != nil {
				stderrLogger().Warn("incremental catch-up failed", "err", err)
			}
		}
	}
	return database, nil
}

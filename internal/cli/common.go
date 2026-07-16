package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
	"github.com/linhn0617/clio/internal/registry"
)

// osExecutable resolves the running binary's path; overridable in tests so
// install-mcp/install-hook can be exercised without depending on the go test
// binary's actual path (which is never named "clio").
var osExecutable = os.Executable

// addSourceFlag registers the shared --source flag (which agent tool's history to
// read), defaulting to the registry's default source (Claude Code) so existing
// behavior is unchanged. Accepted values and their order are derived from the
// source registry rather than hardcoded, so a source added to the registry
// appears in --help without editing this function.
func addSourceFlag(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVar(p, "source", registry.DefaultSource(),
		"Which tool's history: "+strings.Join(registry.EnumValues(), " | "))
}

// validateSource rejects an unknown --source value before it reaches a query.
// The accepted set (registered source names, plus "all") is derived from the
// source registry, not a hardcoded literal list.
func validateSource(s string) error {
	if s == "" || s == registry.All || registry.IsValid(s) {
		return nil
	}
	return fmt.Errorf("invalid --source %q (want %s, or %s)", s, strings.Join(registry.Names(), ", "), registry.All)
}

// nonDefaultSourceAvailable reports whether any registered source other than
// the default (Claude Code) has an available root directory, so a machine
// with only that source (no ~/.claude/projects) can still bootstrap and
// index. Generalized from the former codex-specific check: adding a new
// source to the registry makes this work for it without editing this
// function (design.md D4).
func nonDefaultSourceAvailable() bool {
	return registry.NonDefaultRootAvailable()
}

// bootstrapMissingSourcesError builds the "no sessions to index" error CLI
// bootstrap (index/install-mcp) returns when neither the default source's
// dir (projectsDir) nor any non-default registered source's root is
// available. The non-default clause is generated from the registry
// (registry.NonDefaultRootLabelsProse), not a hardcoded "a Codex sessions
// dir" literal, so an additional registered source is named here without
// editing this function or its callers (codex review P1 finding #1).
func bootstrapMissingSourcesError(projectsDir string, statErr error) error {
	return fmt.Errorf("no sessions to index: neither %s nor %s exists: %w",
		projectsDir, registry.NonDefaultRootLabelsProse(), statErr)
}

func stderrLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openAndCatchUp opens the database for a read-mostly CLI command and performs a
// quick incremental catch-up so results reflect the latest sessions. When a
// live MCP leader is detected via the lock file, the CLI defers to it and
// opens the database read-only instead of running its own catch-up.
func openAndCatchUp() (*db.DB, error) {
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
		// Run the catch-up unconditionally (not gated on the Claude dir existing): on a
		// Codex-only machine ~/.claude/projects is absent, but the Codex source still
		// has history to ingest. IngestAll/PurgeMissing handle missing roots per-source.
		ing := ingest.NewWithBuiltinSources(database, discardLogger())
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
	return database, nil
}

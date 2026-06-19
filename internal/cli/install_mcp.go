package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/claudeconfig"
	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
)

const mcpServerName = "clio"

func newInstallMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install-mcp",
		Short: "Index history and register clio in ~/.claude.json",
		Long:  "Runs a full index first; only if that succeeds does it register clio in ~/.claude.json (atomic write with a .bak backup).",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Phase 1: full ingest. On any failure we do NOT touch the config.
			projects, err := config.ClaudeProjectsDir()
			if err != nil {
				return err
			}
			if _, err := os.Stat(projects); err != nil {
				return fmt.Errorf("claude projects dir not found at %s: %w", projects, err)
			}
			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			// If an MCP server is already running it owns the index; skip the
			// ingest (it's current) and go straight to the config write.
			lockPath, _ := config.LockPath()
			if lockPath != "" && lock.IsHeld(lockPath) {
				fmt.Fprintln(os.Stdout, "clio mcp is already running and indexing; skipping initial index.")
			} else {
				database, err := db.Open(dbPath)
				if err != nil {
					return err
				}
				ing := ingest.New(database, stderrLogger())
				ing.AddCodexSource() // index Codex CLI history too, when installed
				fmt.Fprintln(os.Stdout, "Indexing your Claude Code history…")
				st, err := ing.IngestAll(cmd.Context(), projects, false)
				if err != nil {
					database.Close()
					return fmt.Errorf("indexing failed, leaving ~/.claude.json untouched: %w", err)
				}
				database.Close()
				fmt.Fprintf(os.Stdout, "Indexed %d files, %d messages.\n", st.FilesScanned, st.MessagesAdded)
			}

			// Phase 2: register in ~/.claude.json.
			cfgPath, err := config.ClaudeConfigFile()
			if err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil || exe == "" {
				exe = "clio" // fall back to PATH lookup
			}
			if err := claudeconfig.AddServer(cfgPath, mcpServerName, claudeconfig.ServerEntry{
				Command: exe,
				Args:    []string{"mcp"},
			}); err != nil {
				return fmt.Errorf("register MCP server: %w", err)
			}
			fmt.Fprintf(os.Stdout, "Registered clio MCP server in %s.\nRestart Claude Code, then ask it about your past conversations.\n", cfgPath)
			return nil
		},
	}
}

func newUninstallMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall-mcp",
		Short: "Remove clio from ~/.claude.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, err := config.ClaudeConfigFile()
			if err != nil {
				return err
			}
			has, err := claudeconfig.HasServer(cfgPath, mcpServerName)
			if err != nil {
				return err
			}
			if !has {
				fmt.Fprintln(os.Stdout, "clio is not registered; nothing to do.")
				return nil
			}
			if err := claudeconfig.RemoveServer(cfgPath, mcpServerName); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, "Removed clio MCP server from ~/.claude.json.")
			return nil
		},
	}
}

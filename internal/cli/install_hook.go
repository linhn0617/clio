package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/claudeconfig"
	"github.com/linhn0617/clio/internal/config"
)

func newInstallHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install-hook",
		Short: "Register a SessionStart hook that injects a recall digest into new sessions",
		Long: "Adds a Claude Code SessionStart hook (in ~/.claude/settings.json) that runs " +
			"`clio recall`, so each new session in a known project starts with a digest of its " +
			"recent activity. Opt-in and separate from install-mcp; preserves your existing hooks " +
			"(atomic write; the original is left intact on failure).",
		RunE: func(cmd *cobra.Command, args []string) error {
			settingsPath, err := config.ClaudeSettingsFile()
			if err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil || exe == "" {
				exe = "clio" // fall back to PATH lookup
			}
			if err := claudeconfig.AddSessionStartHook(settingsPath, exe+" recall"); err != nil {
				return fmt.Errorf("register SessionStart hook: %w", err)
			}
			fmt.Fprintf(os.Stdout, "Registered the clio recall SessionStart hook in %s.\nRestart Claude Code; new sessions will open with a recent-activity digest for the project.\n", settingsPath)
			return nil
		},
	}
}

func newUninstallHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall-hook",
		Short: "Remove the clio recall SessionStart hook",
		RunE: func(cmd *cobra.Command, args []string) error {
			settingsPath, err := config.ClaudeSettingsFile()
			if err != nil {
				return err
			}
			has, err := claudeconfig.HasSessionStartHook(settingsPath)
			if err != nil {
				return err
			}
			if !has {
				fmt.Fprintln(os.Stdout, "clio recall hook is not registered; nothing to do.")
				return nil
			}
			if err := claudeconfig.RemoveSessionStartHook(settingsPath); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, "Removed the clio recall SessionStart hook from ~/.claude/settings.json.")
			return nil
		},
	}
}

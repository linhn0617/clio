package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/claudeconfig"
	"github.com/linhn0617/clio/internal/config"
)

func newInstallHookCmd() *cobra.Command {
	var noFileContext bool
	cmd := &cobra.Command{
		Use:   "install-hook",
		Short: "Register clio's Claude Code hooks: recall at session start, file history before each Read or file-reading Bash command",
		Long: "Adds two Claude Code hooks to ~/.claude/settings.json: a SessionStart hook that runs " +
			"`clio recall` (each new session opens with the project's recent activity and where the " +
			"last session left off) and a PreToolUse hook on Read and Bash that runs `clio file-history --hook` " +
			"(Claude sees which past sessions touched a file before reading it; Bash calls only count when the command names an existing file under the working directory). Opt-in and separate " +
			"from install-mcp; idempotent per hook; preserves your existing hooks (atomic write; the " +
			"original is left intact on failure). --no-file-context registers only the recall hook.",
		RunE: func(cmd *cobra.Command, args []string) error {
			settingsPath, err := config.ClaudeSettingsFile()
			if err != nil {
				return err
			}
			exe, err := osExecutable()
			if err != nil || exe == "" {
				exe = "clio" // fall back to PATH lookup
			}
			hadRecall, err := claudeconfig.HasSessionStartHook(settingsPath)
			if err != nil {
				return err
			}
			legacyRecall, err := claudeconfig.HasLegacySessionStartHook(settingsPath)
			if err != nil {
				return err
			}
			hadFile, err := claudeconfig.HasPreToolUseReadHook(settingsPath)
			if err != nil {
				return err
			}
			legacyFile, err := claudeconfig.HasLegacyPreToolUseReadHook(settingsPath)
			if err != nil {
				return err
			}
			unquoted, err := claudeconfig.HasUnquotedClioHook(settingsPath)
			if err != nil {
				return err
			}
			// The hook command runs through a shell, so a path with spaces must be
			// quoted; plain paths stay unquoted to keep existing installs identical.
			exe = claudeconfig.ShellQuote(exe)
			fileHistoryCmd := exe + " file-history --hook"
			if noFileContext {
				fileHistoryCmd = "" // also removes a previously installed file-history hook
			}
			// One mutation for both hooks: the settings .bak stays the pre-install
			// file and a failure leaves neither hook half-registered.
			if err := claudeconfig.AddClioHooks(settingsPath, exe+" recall", fileHistoryCmd); err != nil {
				return fmt.Errorf("register hooks: %w", err)
			}
			var added []string
			if !hadRecall {
				added = append(added, "the recall SessionStart hook")
			}
			if !noFileContext && !hadFile {
				added = append(added, "the file-history PreToolUse(Read|Bash) hook")
			}
			out := cmd.OutOrStdout()
			removedFile := noFileContext && hadFile
			upgradedFile := legacyFile && !noFileContext
			switch {
			case len(added) > 0:
				fmt.Fprintf(out, "Registered %s in %s.\n", strings.Join(added, " and "), settingsPath)
			case !removedFile && !legacyRecall && !unquoted && !upgradedFile:
				fmt.Fprintf(out, "clio hooks already registered in %s; nothing to add.\n", settingsPath)
			}
			if legacyRecall {
				fmt.Fprintln(out, "Upgraded the recall SessionStart hook (skips forked sessions; 10 s timeout).")
			}
			if upgradedFile {
				fmt.Fprintln(out, "Upgraded the file-history PreToolUse hook from Read to Read|Bash.")
			}
			if unquoted {
				fmt.Fprintln(out, "Upgraded a clio hook whose binary path contained a space to the quoted current path.")
			}
			if removedFile {
				fmt.Fprintln(out, "Removed the file-history PreToolUse hook (--no-file-context).")
			}
			fmt.Fprintln(out, "Restart Claude Code for the hooks to take effect.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&noFileContext, "no-file-context", false, "Register only the SessionStart recall hook (skip the PreToolUse file-history hook)")
	return cmd
}

func newUninstallHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall-hook",
		Short: "Remove clio's SessionStart and PreToolUse hooks",
		RunE: func(cmd *cobra.Command, args []string) error {
			settingsPath, err := config.ClaudeSettingsFile()
			if err != nil {
				return err
			}
			// Unscoped detection: an entry a user moved under another matcher would
			// never fire, but it is still ours to clean up.
			hasRecall, hasFile, err := claudeconfig.HasAnyClioHookEntries(settingsPath)
			if err != nil {
				return err
			}
			if !hasRecall && !hasFile {
				fmt.Fprintln(cmd.OutOrStdout(), "clio hooks are not registered; nothing to do.")
				return nil
			}
			var removed []string
			if hasRecall {
				removed = append(removed, "recall SessionStart")
			}
			if hasFile {
				removed = append(removed, "file-history PreToolUse")
			}
			if err := claudeconfig.RemoveClioHooks(settingsPath); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed the clio %s hook(s) from ~/.claude/settings.json.\n", strings.Join(removed, " and "))
			return nil
		},
	}
}

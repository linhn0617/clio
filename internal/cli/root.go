// Package cli wires up clio's cobra command tree.
package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// version is set via -ldflags at build time.
var version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "clio",
		Short:         "Search and browse your Claude Code conversation history",
		Long:          "clio indexes Claude Code session history (~/.claude/projects/*.jsonl) into SQLite + FTS5 and exposes it via CLI and an MCP server.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newIndexCmd(),
		newSearchCmd(),
		newListCmd(),
		newShowCmd(),
		newActivityCmd(),
		newAskCmd(),
		newTUICmd(),
		newRecallCmd(),
		newMCPCmd(),
		newDoctorCmd(),
		newInstallMCPCmd(),
		newUninstallMCPCmd(),
		newInstallHookCmd(),
		newUninstallHookCmd(),
	)
	return root
}

// Execute runs the clio CLI with a context cancelled on SIGINT/SIGTERM, so
// long-running commands (notably `clio mcp`) can shut down their goroutines.
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return newRootCmd().ExecuteContext(ctx)
}

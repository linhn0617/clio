// Package cli wires up clio's cobra command tree.
package cli

import (
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
		newMCPCmd(),
		newDoctorCmd(),
		newInstallMCPCmd(),
		newUninstallMCPCmd(),
	)
	return root
}

// Execute runs the clio CLI.
func Execute() error {
	return newRootCmd().Execute()
}

package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the stdio MCP server for Claude Code",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("mcp: not implemented yet (Phase 3)")
		},
	}
}

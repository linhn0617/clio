package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newIndexCmd() *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Scan and index Claude Code session history",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("index: not implemented yet (Phase 1)")
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "Force a full re-index instead of incremental")
	return cmd
}

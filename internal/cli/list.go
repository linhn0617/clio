package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sessions with optional filters",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("list: not implemented yet (Phase 2)")
		},
	}
	cmd.Flags().String("since", "", "Only sessions since this time (e.g. 7d, 2026-05-01)")
	cmd.Flags().String("project", "", "Filter by project path prefix")
	cmd.Flags().Int("min-turns", 0, "Only sessions with at least this many turns")
	cmd.Flags().Int("limit", 50, "Maximum number of sessions")
	cmd.Flags().Bool("json", false, "Output JSON")
	return cmd
}

package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search across all conversations",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("search: not implemented yet (Phase 2)")
		},
	}
	cmd.Flags().String("since", "", "Only results since this time (e.g. 7d, 2026-05-01)")
	cmd.Flags().String("project", "", "Filter by project path prefix")
	cmd.Flags().String("role", "", "Filter by role (user|assistant)")
	cmd.Flags().Int("limit", 20, "Maximum number of results")
	cmd.Flags().Bool("json", false, "Output JSON")
	cmd.Flags().Bool("include-tool-output", false, "Include tool output in results")
	return cmd
}

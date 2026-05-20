package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <session-uuid-or-prefix>",
		Short: "Print a full session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("show: not implemented yet (Phase 2)")
		},
	}
	cmd.Flags().String("format", "markdown", "Output format (markdown|json|raw)")
	cmd.Flags().Bool("no-tool-output", false, "Omit tool output")
	return cmd
}

package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newInstallMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install-mcp",
		Short: "Index history and register clio in ~/.claude.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("install-mcp: not implemented yet (Phase 4)")
		},
	}
}

func newUninstallMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall-mcp",
		Short: "Remove clio from ~/.claude.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("uninstall-mcp: not implemented yet (Phase 4)")
		},
	}
}

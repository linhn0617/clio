package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose paths, DB integrity, and ingest health",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("doctor: not implemented yet (Phase 4)")
		},
	}
}

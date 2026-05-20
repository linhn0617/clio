package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/doctor"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose paths, DB integrity, and ingest health",
		RunE: func(cmd *cobra.Command, args []string) error {
			projects, _ := config.ClaudeProjectsDir()
			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			if _, err := os.Stat(dbPath); err != nil {
				return fmt.Errorf("no index found at %s — run `clio index` first", dbPath)
			}
			database, err := db.OpenReadOnly(dbPath)
			if err != nil {
				return err
			}
			defer database.Close()

			results := doctor.Run(database, projects, dbPath)
			allOK := true
			for _, r := range results {
				mark := "ok  "
				if !r.OK {
					mark = "WARN"
					allOK = false
				}
				fmt.Fprintf(os.Stdout, "[%s] %-22s %s\n", mark, r.Name, r.Detail)
			}
			if !allOK {
				fmt.Fprintln(os.Stdout, "\nSome checks reported warnings. Run `clio index --full` to rebuild if needed.")
			}
			return nil
		},
	}
}

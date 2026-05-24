package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/doctor"
)

var errChecksFailed = errors.New("doctor: some checks reported warnings")

func reportDoctor(w io.Writer, results []doctor.Result) error {
	allOK := true
	for _, r := range results {
		mark := "ok  "
		if !r.OK {
			mark = "WARN"
			allOK = false
		}
		fmt.Fprintf(w, "[%s] %-22s %s\n", mark, r.Name, r.Detail)
	}
	if !allOK {
		fmt.Fprintln(w, "\nSome checks reported warnings. Run `clio index --full` to rebuild if needed.")
		return errChecksFailed
	}
	return nil
}

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

			return reportDoctor(os.Stdout, doctor.Run(database, projects, dbPath))
		},
	}
}

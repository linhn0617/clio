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
	allOK, indexFailed := true, false
	for _, r := range results {
		mark := "ok  "
		if r.Skipped {
			mark = "skip"
		} else if !r.OK {
			mark = "WARN"
			allOK = false
			if !r.Integration {
				indexFailed = true
			}
		}
		fmt.Fprintf(w, "[%s] %-22s %s\n", mark, r.Name, r.Detail)
	}
	if !allOK {
		// Integration failures state their own remedy in the detail; rebuilding the
		// index would not fix a stale hook path.
		if indexFailed {
			fmt.Fprintln(w, "\nSome checks reported warnings. Run `clio index --full` to rebuild if needed.")
		} else {
			fmt.Fprintln(w, "\nSome checks reported warnings; see the remedy on each WARN line.")
		}
		return errChecksFailed
	}
	return nil
}

func newDoctorCmd() *cobra.Command {
	var quick bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose paths, DB integrity, and ingest health",
		Long: "Diagnose paths, DB integrity, ingest health, and the Claude Code integration (hooks, MCP registration, lease, index freshness).\n\n" +
			"--quick skips the whole-database scans (db integrity, the FTS content check, FTS bloat), which take about a minute on a large index; the FTS row-count comparison and every other check still run. Use plain doctor for a deep check.",
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

			opts := doctor.Options{Quick: quick}
			// Each path is resolved on its own; one that fails stays empty and only
			// its check reports it could not resolve the path.
			opts.Integration = true
			opts.SettingsPath, _ = config.ClaudeSettingsFile()
			opts.ClaudeConfigPath, _ = config.ClaudeConfigFile()
			opts.LockPath, _ = config.LockPath()
			return reportDoctor(os.Stdout, doctor.RunWith(database, projects, dbPath, opts))
		},
	}
	cmd.Flags().BoolVar(&quick, "quick", false, "skip the whole-database scans (db integrity, FTS content check, FTS bloat); FTS row counts are still compared")
	return cmd
}

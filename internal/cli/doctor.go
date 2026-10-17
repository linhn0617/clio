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
		if r.Skipped {
			mark = "skip"
		} else if !r.OK {
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
	var quick bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose paths, DB integrity, and ingest health",
		Long: "Diagnose paths, DB integrity, ingest health, and the Claude Code integration (hooks, MCP registration, lease, index freshness).\n\n" +
			"--quick runs PRAGMA quick_check and skips the full integrity_check, the FTS content check and the FTS bloat measurement, which scan the whole database and take about a minute on a large index.",
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
			// A path that cannot be resolved (no HOME) leaves its check out rather
			// than failing the whole run.
			settings, e1 := config.ClaudeSettingsFile()
			cfg, e2 := config.ClaudeConfigFile()
			lockPath, e3 := config.LockPath()
			if e1 == nil && e2 == nil && e3 == nil {
				opts.Integration = true
				opts.SettingsPath, opts.ClaudeConfigPath, opts.LockPath = settings, cfg, lockPath
			}
			return reportDoctor(os.Stdout, doctor.RunWith(database, projects, dbPath, opts))
		},
	}
	cmd.Flags().BoolVar(&quick, "quick", false, "skip the whole-database scans (integrity_check, FTS content check, FTS bloat); run quick_check instead")
	return cmd
}

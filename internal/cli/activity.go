package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/sessions"
)

func newActivityCmd() *cobra.Command {
	var (
		by      string
		since   string
		project string
		limit   int
		asJSON  bool
		source  string
	)
	cmd := &cobra.Command{
		Use:   "activity",
		Short: "Summarize indexed activity (files, commands, tools) grouped by kind",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateSource(source); err != nil {
				return err
			}
			switch by {
			case "file", "command", "tool", "pattern", "url":
			default:
				return fmt.Errorf("--by must be one of file|command|tool|pattern|url")
			}
			sinceTS, err := parseSince(since)
			if err != nil {
				return err
			}
			database, err := openAndCatchUp()
			if err != nil {
				return err
			}
			defer database.Close()

			rows, err := sessions.ActivityByKind(cmd.Context(), database, by, sinceTS, project, source, limit)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "no activity")
				return nil
			}
			for _, r := range rows {
				fmt.Fprintf(os.Stdout, "%6d  %s\n", r.Count, r.Value)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&by, "by", "tool", "Group by: file|command|tool|pattern|url")
	cmd.Flags().StringVar(&since, "since", "", "Only activity since this time (e.g. 7d)")
	cmd.Flags().StringVar(&project, "project", "", "Filter by project path prefix")
	cmd.Flags().IntVar(&limit, "limit", 30, "Maximum number of rows")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	addSourceFlag(cmd, &source)
	return cmd
}

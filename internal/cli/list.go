package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/sessions"
)

func newListCmd() *cobra.Command {
	var (
		since            string
		project          string
		minTurns         int
		limit            int
		asJSON           bool
		touched          string
		tool             string
		ran              string
		includeSubagents bool
		source           string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sessions with optional filters",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateSource(source); err != nil {
				return err
			}
			sinceTS, err := parseSince(since)
			if err != nil {
				return err
			}
			database, err := openForQuery()
			if err != nil {
				return err
			}
			defer database.Close()

			rows, err := sessions.ListSessions(cmd.Context(), database, sessions.ListFilter{
				Since:            sinceTS,
				ProjectPrefix:    project,
				MinTurns:         minTurns,
				Limit:            limit,
				Touched:          touched,
				Tool:             tool,
				Ran:              ran,
				IncludeSubagents: includeSubagents,
				Source:           source,
			})
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "no sessions")
				return nil
			}
			for _, s := range rows {
				fmt.Fprintln(os.Stdout, formatSessionLine(s))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "Only sessions since this time (e.g. 7d)")
	cmd.Flags().StringVar(&project, "project", "", "Filter by project path prefix")
	cmd.Flags().IntVar(&minTurns, "min-turns", 0, "Only sessions with at least this many turns")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of sessions")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	cmd.Flags().StringVar(&touched, "touched", "", "Only sessions whose tool calls touched this path prefix")
	cmd.Flags().StringVar(&tool, "tool", "", "Only sessions that used this tool (exact name)")
	cmd.Flags().StringVar(&ran, "ran", "", "Only sessions that ran a command containing this substring")
	cmd.Flags().BoolVar(&includeSubagents, "include-subagents", false, "Include subagent child sessions (default: top-level only)")
	addSourceFlag(cmd, &source)
	return cmd
}

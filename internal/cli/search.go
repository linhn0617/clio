package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/search"
)

func newSearchCmd() *cobra.Command {
	var (
		since     string
		project   string
		role      string
		limit     int
		asJSON    bool
		inclTools bool
		touched   string
		tool      string
		ran       string
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search across all conversations",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sinceTS, err := parseSince(since)
			if err != nil {
				return err
			}
			database, err := openForQuery()
			if err != nil {
				return err
			}
			defer database.Close()

			results, err := search.Search(cmd.Context(), database, search.Options{
				Query:             strings.Join(args, " "),
				Since:             sinceTS,
				ProjectPrefix:     project,
				Role:              role,
				Limit:             limit,
				IncludeToolOutput: inclTools,
				Touched:           touched,
				Tool:              tool,
				Ran:               ran,
			})
			if err != nil {
				return err
			}

			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(results)
			}
			if len(results) == 0 {
				fmt.Fprintln(os.Stdout, "no results")
				return nil
			}
			for _, r := range results {
				fmt.Fprintln(os.Stdout, formatSearchResult(r))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "Only results since this time (e.g. 7d, 2026-05-01)")
	cmd.Flags().StringVar(&project, "project", "", "Filter by project path prefix")
	cmd.Flags().StringVar(&role, "role", "", "Filter by role (user|assistant)")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of results")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	cmd.Flags().BoolVar(&inclTools, "include-tool-output", false, "Include tool output in results")
	cmd.Flags().StringVar(&touched, "touched", "", "Only sessions whose tool calls touched this path prefix")
	cmd.Flags().StringVar(&tool, "tool", "", "Only sessions that used this tool (exact name)")
	cmd.Flags().StringVar(&ran, "ran", "", "Only sessions that ran a command containing this substring")
	return cmd
}

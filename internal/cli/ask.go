package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/ask"
	"github.com/linhn0617/clio/internal/config"
)

func newAskCmd() *cobra.Command {
	var (
		since   string
		project string
		limit   int
		window  int
		asJSON  bool
		source  string
	)
	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Answer a question from history — a cited, windowed evidence bundle (no generation)",
		Long: "Retrieve the conversation excerpts most relevant to a question, each with a window of " +
			"surrounding turns, grouped by session and cited. clio generates no answer and makes no " +
			"network call; over MCP, Claude synthesizes from the bundle. By default it searches all " +
			"projects.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			question := strings.TrimSpace(strings.Join(args, " "))
			if question == "" {
				return fmt.Errorf("a question is required")
			}
			if err := validateSource(source); err != nil {
				return err
			}
			sinceTS, err := parseSince(since)
			if err != nil {
				return err
			}

			// A missing index is a clean empty result, not an error (e.g. a fresh
			// install that has not indexed yet); any other stat failure surfaces.
			ans := ask.Answer{Question: question, Groups: []ask.EvidenceGroup{}}
			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			if _, statErr := os.Stat(dbPath); statErr == nil {
				// Like search/list/show: a quick incremental catch-up so the answer
				// reflects the latest sessions; openAndCatchUp defers to a live MCP
				// server (opens read-only) to avoid write contention.
				database, err := openAndCatchUp()
				if err != nil {
					return err
				}
				defer database.Close()
				ans, err = ask.Ask(cmd.Context(), database, ask.Options{
					Question:      question,
					ProjectPrefix: project,
					Since:         sinceTS,
					MaxSessions:   limit,
					Window:        window,
					Source:        source,
				})
				if err != nil {
					return err
				}
			} else if !errors.Is(statErr, fs.ErrNotExist) {
				return statErr
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(ans)
			}
			writeAnswer(os.Stdout, ans, source)
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "Only consider sessions since this time (e.g. 7d, 2026-05-01)")
	cmd.Flags().StringVar(&project, "project", "", "Limit to a project path prefix (default: all projects)")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum sessions in the bundle (default 6)")
	cmd.Flags().IntVar(&window, "window", 0, "Dialogue turns to include each side of a match (default 2)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output the bundle as JSON")
	addSourceFlag(cmd, &source)
	return cmd
}

// writeAnswer renders the evidence bundle as a readable, grouped digest: a
// citation header per session, then its windowed excerpts with matched lines
// marked. No prose answer is produced — that is the caller's to synthesize.
// source is the --source value the bundle was queried with; the citation header
// is tagged with each group's originating tool only when source is "all" —
// otherwise every group shares the same (requested) source and the tag is noise,
// matching how `clio search`'s text output omits it for a single-source query.
func writeAnswer(w io.Writer, ans ask.Answer, source string) {
	if len(ans.Groups) == 0 {
		fmt.Fprintln(w, "no relevant history found")
		return
	}
	fmt.Fprintf(w, "clio ask — %q\n", ans.Question)
	for _, g := range ans.Groups {
		tag := ""
		if source == "all" && g.Source != "" {
			tag = "  [" + g.Source + "]"
		}
		fmt.Fprintf(w, "\n[%s] %s  ·  %s  ·  %s%s\n",
			shortID(g.SessionUUID), oneLine(g.Title, 60), trimProject(g.Project), formatTS(g.EndedAt), tag)
		for _, e := range g.Excerpts {
			marker := "    "
			if e.IsHit {
				marker = "  » "
			}
			fmt.Fprintf(w, "%s[%s] %s\n", marker, e.Role, oneLine(e.Text, 200))
		}
	}
	fmt.Fprintln(w, "\nNo answer is generated; synthesize from the excerpts above, or open one with `clio show <id>`.")
}

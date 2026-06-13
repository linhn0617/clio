package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/sessions"
)

func newRecallCmd() *cobra.Command {
	var (
		project    string
		since      string
		limit      int
		noCommands bool
	)
	cmd := &cobra.Command{
		Use:   "recall",
		Short: "Print a recent-activity digest for the current project (used by the SessionStart hook)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Never break session startup: any failure yields empty output, exit 0.
			if out := recallDigest(project, since, limit, noCommands); out != "" {
				fmt.Fprint(os.Stdout, out)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project path prefix (default: detected from the working directory)")
	cmd.Flags().StringVar(&since, "since", "14d", "Look back this far for touched files and run commands")
	cmd.Flags().IntVar(&limit, "limit", 5, "Max items per section")
	cmd.Flags().BoolVar(&noCommands, "no-commands", false, "Omit the recent-commands section")
	return cmd
}

// recallDigest builds the digest text, swallowing every error (returns "" on any
// problem) so the SessionStart hook can never break Claude Code startup.
func recallDigest(project, since string, limit int, noCommands bool) string {
	if project == "" {
		project = detectProject()
	}
	if project == "" {
		return ""
	}
	dbPath, err := config.DBPath()
	if err != nil {
		return ""
	}
	if _, err := os.Stat(dbPath); err != nil {
		return ""
	}
	database, err := db.OpenReadOnly(dbPath)
	if err != nil {
		return ""
	}
	defer database.Close()

	var sinceTS int64
	if ts, err := parseSince(since); err == nil {
		sinceTS = ts
	}
	r, err := sessions.GetRecall(context.Background(), database, project, sinceTS, limit, limit)
	if err != nil {
		return ""
	}
	if noCommands {
		r.Commands = nil
	}
	return formatRecall(project, r)
}

// detectProject resolves the current project: the SessionStart hook pipes a JSON
// payload with `cwd` on stdin; a manual run falls back to the working directory.
func detectProject() string {
	cwd := parseHookCwd(readStdinIfPiped())
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if cwd == "" {
		return ""
	}
	return projectRoot(cwd)
}

// projectRoot walks up from dir to the nearest ancestor containing a .git entry
// (a directory in a normal repo, a file in a worktree/submodule), so recall
// scopes to the whole repo even when Claude Code is launched from a subdirectory;
// it falls back to dir when no repo root is found.
func projectRoot(dir string) string {
	for d := dir; d != ""; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return dir
}

// readStdinIfPiped reads stdin only when it is piped (not a TTY), so a manual
// `clio recall` in a terminal never blocks waiting for input.
func readStdinIfPiped() []byte {
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) != 0 {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(os.Stdin, 64*1024))
	return data
}

// parseHookCwd extracts `cwd` from a Claude Code hook JSON payload.
func parseHookCwd(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	var p struct {
		Cwd string `json:"cwd"`
	}
	if json.Unmarshal(data, &p) != nil {
		return ""
	}
	return p.Cwd
}

// formatRecall renders the digest, or "" when there is nothing to recall.
func formatRecall(project string, r sessions.Recall) string {
	if len(r.Sessions) == 0 && len(r.Files) == 0 && len(r.Commands) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "clio — recent activity in %s\n", project)
	if len(r.Sessions) > 0 {
		b.WriteString("Recent sessions:\n")
		for _, s := range r.Sessions {
			fmt.Fprintf(&b, "  - %s  %s (%d turns)\n", formatTS(s.EndedAt), oneLine(s.Title, 70), s.TurnCount)
		}
	}
	if len(r.Files) > 0 {
		b.WriteString("Recently touched files:\n")
		for _, f := range r.Files {
			fmt.Fprintf(&b, "  - %s (%dx)\n", f.Value, f.Count)
		}
	}
	if len(r.Commands) > 0 {
		b.WriteString("Recently run commands:\n")
		for _, c := range r.Commands {
			fmt.Fprintf(&b, "  - %s (%dx)\n", oneLine(c.Value, 80), c.Count)
		}
	}
	b.WriteString("(Search older history with the clio MCP tools or `clio search`.)\n")
	return b.String()
}

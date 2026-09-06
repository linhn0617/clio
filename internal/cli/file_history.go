package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/sessions"
)

func newFileHistoryCmd() *cobra.Command {
	var (
		since string
		limit int
		hook  bool
	)
	cmd := &cobra.Command{
		Use:   "file-history <path>",
		Short: "Print the past sessions that touched one file (newest first)",
		Long: "Lists the Claude Code sessions whose tool calls read or edited the given file, " +
			"newest first, with the tools each used. Read-only; prints nothing and exits 0 when " +
			"the file has no indexed history or the index is unavailable.",
		Args: func(cmd *cobra.Command, args []string) error {
			if hook {
				return nil // hook mode takes the path from stdin; never a usage error
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath, err := config.DBPath()
			if err != nil {
				return nil
			}
			if hook {
				if out := fileHistoryHook(readStdinIfPiped(), dbPath, limit); out != "" {
					fmt.Fprint(os.Stdout, out)
				}
				return nil
			}
			// A bad --since is a usage error in CLI mode; silently widening the
			// window would hide that the flag did nothing.
			sinceTS, err := parseSince(since)
			if err != nil {
				return err
			}
			key := fileHistoryKey(args[0])
			if out := fileHistoryDigest(dbPath, key, "", sinceTS, limit); out != "" {
				fmt.Fprint(os.Stdout, out)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "Only touches since this time: 7d, 12h, or YYYY-MM-DD (default: unbounded)")
	cmd.Flags().IntVar(&limit, "limit", 10, "Max sessions to list")
	cmd.Flags().BoolVar(&hook, "hook", false, "PreToolUse hook mode: read the Claude Code payload from stdin and answer in hook JSON")
	return cmd
}

// hookMaxChars caps the injected digest below Claude Code's 10,000-character
// hook-output limit, past which Claude only receives a file path.
const hookMaxChars = 8000

// hookPayload is the subset of the PreToolUse stdin JSON the hook reads.
type hookPayload struct {
	ToolName  string `json:"tool_name"`
	SessionID string `json:"session_id"`
	ToolInput struct {
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
}

// fileHistoryHook answers a PreToolUse payload: the file's past-session timeline
// as additional context, or "" (nothing to say, or any failure). It never emits
// a permission decision, so the Read proceeds through the normal flow.
func fileHistoryHook(payload []byte, dbPath string, limit int) string {
	var p hookPayload
	if len(payload) == 0 || json.Unmarshal(payload, &p) != nil {
		return ""
	}
	if p.ToolName != "Read" || strings.TrimSpace(p.ToolInput.FilePath) == "" {
		return ""
	}
	key := fileHistoryKey(p.ToolInput.FilePath)
	rows, newest, err := fileHistoryRows(dbPath, key, p.SessionID, 0, limit)
	if err != nil || len(rows) == 0 {
		return ""
	}
	// Staleness gate: a file edited after its newest indexed touch would get a
	// misleading timeline. A failed stat (file not on disk yet) skips the gate.
	if fi, err := os.Stat(p.ToolInput.FilePath); err == nil && fi.ModTime().Unix() > newest {
		return ""
	}
	digest := formatFileHistoryCapped(p.ToolInput.FilePath, rows, hookMaxChars)
	if digest == "" {
		return "" // nothing fits the cap: stay silent rather than send an empty context
	}
	env := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PreToolUse",
			"additionalContext": digest,
		},
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep `<id>` readable; Claude Code parses the JSON either way
	if err := enc.Encode(env); err != nil {
		return ""
	}
	return buf.String()
}

// formatFileHistoryCapped is formatFileHistory bounded to maxChars: rows are
// dropped from the tail and announced with a "(+N more)" line.
func formatFileHistoryCapped(path string, rows []sessions.FileTouch, maxChars int) string {
	full := formatFileHistory(path, rows)
	if len([]rune(full)) <= maxChars {
		return full
	}
	for n := len(rows) - 1; n > 0; n-- {
		kept := formatFileHistory(path, rows[:n])
		more := fmt.Sprintf("  (+%d more)\n", len(rows)-n)
		if len([]rune(kept))+len([]rune(more)) <= maxChars {
			// Insert the marker before the trailing pointer line.
			i := strings.LastIndex(kept, "(Details:")
			return kept[:i] + more + kept[i:]
		}
	}
	return ""
}

// fileHistoryKey turns a user-supplied path into the exact lookup key the
// ingester stored: absolute, then the same redaction and byte cap.
func fileHistoryKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return ingest.NormalizeTargetPath(path)
}

// fileHistoryDigest renders the timeline, swallowing every error (returns ""
// on any problem) so the PreToolUse hook can never disturb a Read.
func fileHistoryDigest(dbPath, key, excludeSession string, since int64, limit int) string {
	rows, _, err := fileHistoryRows(dbPath, key, excludeSession, since, limit)
	if err != nil {
		return ""
	}
	return formatFileHistory(key, rows)
}

// fileHistoryRows opens the index read-only and runs the lookup; the newest
// touch is returned alongside the rows for the hook's staleness gate.
func fileHistoryRows(dbPath, key, excludeSession string, since int64, limit int) ([]sessions.FileTouch, int64, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, 0, err
	}
	database, err := db.OpenReadOnly(dbPath)
	if err != nil {
		return nil, 0, err
	}
	defer database.Close()
	return sessions.FileHistory(context.Background(), database, key, excludeSession, since, limit)
}

// formatFileHistory renders one row per session, newest first, or "" when
// there is nothing to say.
func formatFileHistory(path string, rows []sessions.FileTouch) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "clio — past sessions that touched %s\n", path)
	for _, r := range rows {
		when := "(undated)       "
		if r.TS > 0 {
			when = formatTS(r.TS)
		}
		fmt.Fprintf(&b, "  - %s  %s (%s)  %s\n", when, oneLine(r.Title, 60), shortID(r.SessionUUID), strings.Join(r.Tools, ","))
	}
	b.WriteString("(Details: clio show <id>, or the clio MCP read_session tool.)\n")
	return b.String()
}

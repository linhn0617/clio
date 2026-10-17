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
	"github.com/linhn0617/clio/internal/termsafe"
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
	Cwd       string `json:"cwd"`
	ToolInput struct {
		FilePath string `json:"file_path"` // Read
		Command  string `json:"command"`   // Bash
	} `json:"tool_input"`
}

// bashMaxFiles bounds how many candidate files one Bash command may query, so
// the hook's DB cost stays at a small constant per call.
const bashMaxFiles = 3

// historyPointer closes every digest; with several files it is emitted once.
const historyPointer = "(Details: clio show <id>, or the clio MCP read_session tool.)\n"

// hookSegment is one file's timeline, ready to be formatted.
type hookSegment struct {
	path string
	rows []sessions.FileTouch
}

// fileHistoryHook answers a PreToolUse payload: the touched files' past-session
// timeline as additional context, or "" (nothing to say, or any failure). It
// never emits a permission decision, so the tool call proceeds through the
// normal flow. Read looks up its file_path; Bash looks up the (at most
// bashMaxFiles) existing files under cwd that the command names.
func fileHistoryHook(payload []byte, dbPath string, limit int) string {
	var p hookPayload
	if len(payload) == 0 || json.Unmarshal(payload, &p) != nil {
		return ""
	}
	var files []string
	switch p.ToolName {
	case "Read":
		if strings.TrimSpace(p.ToolInput.FilePath) != "" {
			files = []string{p.ToolInput.FilePath}
		}
	case "Bash":
		files = bashFileCandidates(p.ToolInput.Command, p.Cwd, bashMaxFiles)
	}
	// Zero candidates return before any DB access: most Bash calls end here.
	var segs []hookSegment
	for _, f := range files {
		rows, newest, err := fileHistoryRows(dbPath, fileHistoryKey(f), p.SessionID, 0, limit)
		if err != nil || len(rows) == 0 {
			continue
		}
		// Staleness gate: a file edited after its newest indexed touch would get a
		// misleading timeline. A failed stat (file not on disk yet) skips the gate.
		if fi, err := os.Stat(f); err == nil && fi.ModTime().Unix() > newest {
			continue
		}
		segs = append(segs, hookSegment{f, rows})
	}
	if len(segs) == 0 {
		return ""
	}
	// The cap must hold on the ENCODED envelope: JSON escaping can expand a
	// pathological path (control characters → \uXXXX) well past the digest's
	// own length, and Claude Code measures its 10,000-character limit on what
	// it receives. Shrink the digest until the encoding fits.
	for budget := hookMaxChars; budget > 0; budget = budget * 3 / 4 {
		var parts []string
		for _, s := range segs {
			// The budget is split evenly; a file whose segment cannot fit its
			// share is dropped rather than starving the others.
			if d := formatFileHistoryCapped(s.path, s.rows, budget/len(segs)); d != "" {
				parts = append(parts, strings.TrimSuffix(d, historyPointer))
			}
		}
		if len(parts) == 0 {
			return "" // nothing fits: stay silent rather than send an empty context
		}
		out := encodeHookEnvelope(strings.Join(parts, "") + historyPointer)
		if out == "" {
			return ""
		}
		if len([]rune(out)) <= hookMaxChars {
			return out
		}
	}
	return ""
}

// encodeHookEnvelope wraps the digest in Claude Code's PreToolUse output shape.
func encodeHookEnvelope(digest string) string {
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
			i := strings.LastIndex(kept, historyPointer)
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
	fmt.Fprintf(&b, "clio — past sessions that touched %s\n", termsafe.Strip(path))
	for _, r := range rows {
		when := "(undated)       "
		if r.TS > 0 {
			when = formatTS(r.TS)
		}
		fmt.Fprintf(&b, "  - %s  %s (%s)  %s\n", when, oneLine(r.Title, 60), shortID(r.SessionUUID), termsafe.Strip(strings.Join(r.Tools, ",")))
	}
	b.WriteString(historyPointer)
	return b.String()
}

// bashFileCandidates returns up to max distinct existing regular files under cwd
// that command names, in first-appearance order. It is a cheap lexical filter,
// not a shell parser: a false candidate costs one DB lookup, a miss costs
// nothing but the context hint. Relative tokens resolve against cwd; an empty or
// relative cwd yields nothing.
// debt: 沒做 $VAR／~／glob 展開、反斜線跳脫；若使用者常以 $PWD/x 或 glob 讀檔就要補
func bashFileCandidates(command, cwd string, max int) []string {
	if !filepath.IsAbs(cwd) || strings.TrimSpace(command) == "" {
		return nil
	}
	cwd = filepath.Clean(cwd)
	var out []string
	seen := map[string]bool{}
	for _, tok := range splitShellWords(command) {
		tok = strings.Trim(tok, ",:")
		if tok == "" || tok[0] == '-' || tok[0] == '$' {
			continue
		}
		if !filepath.IsAbs(tok) {
			tok = filepath.Join(cwd, tok)
		}
		tok = filepath.Clean(tok)
		if seen[tok] || !pathUnder(cwd, tok) {
			continue
		}
		seen[tok] = true
		if fi, err := os.Stat(tok); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		out = append(out, tok)
		if len(out) == max {
			break
		}
	}
	return out
}

// pathUnder reports whether path is strictly inside root, by path elements
// (so /p2 is not under /p).
func pathUnder(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// splitShellWords splits on whitespace and shell operators, treating '…' and "…" as grouping (the
// quotes themselves are dropped) so a path containing spaces stays one word.
// An unterminated quote swallows the rest of the command, which at worst
// produces a word that matches no file.
func splitShellWords(s string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	flush := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || strings.ContainsRune(";|&()<>", r):
			// Operators split words even without spaces (`cat a;cat b`, `x>out`);
			// an unquoted path that really contains one is not matched.
			flush()
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	flush()
	return words
}

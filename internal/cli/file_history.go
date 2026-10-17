package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/sessions"
	"github.com/linhn0617/clio/internal/shellwords"
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

// bashMaxTokens bounds how many words of a Bash command are examined: a stat per
// word on a pathological command line must not stretch the hook's latency.
const bashMaxTokens = 64

// candidateFile is a file the hook may look up; info (when the file exists) is
// kept from candidate selection so the staleness gate needs no second stat.
type candidateFile struct {
	path string
	info os.FileInfo
}

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
	return fileHistoryHookWith(payload, dbPath, limit, db.OpenReadOnly)
}

// fileHistoryHookWith is fileHistoryHook with the index opener injected: a
// read-only open leaves no trace on disk, so tests count opens to prove the
// zero-candidate path never touches the index.
func fileHistoryHookWith(payload []byte, dbPath string, limit int, open func(string) (*db.DB, error)) string {
	var p hookPayload
	if len(payload) == 0 || json.Unmarshal(payload, &p) != nil {
		return ""
	}
	var files []candidateFile
	switch p.ToolName {
	case "Read":
		if strings.TrimSpace(p.ToolInput.FilePath) != "" {
			fi, _ := os.Stat(p.ToolInput.FilePath) // nil on failure: the gate is skipped
			files = []candidateFile{{p.ToolInput.FilePath, fi}}
		}
	case "Bash":
		files = bashFileCandidates(p.ToolInput.Command, p.Cwd, bashMaxFiles)
	}
	// Zero candidates return before any DB access: most Bash calls end here.
	if len(files) == 0 {
		return ""
	}
	if _, err := os.Stat(dbPath); err != nil {
		return ""
	}
	database, err := open(dbPath)
	if err != nil {
		return ""
	}
	defer database.Close()
	var segs []hookSegment
	for _, f := range files {
		rows, newest, err := sessions.FileHistory(context.Background(), database, fileHistoryKey(f.path), p.SessionID, 0, limit)
		if err != nil || len(rows) == 0 {
			continue
		}
		// Staleness gate: a file edited after its newest indexed touch would get a
		// misleading timeline.
		if f.info != nil && f.info.ModTime().Unix() > newest {
			continue
		}
		segs = append(segs, hookSegment{f.path, rows})
	}
	if len(segs) == 0 {
		return ""
	}
	// The cap must hold on the ENCODED envelope: JSON escaping can expand a
	// pathological path (control characters → \uXXXX) well past the digest's
	// own length, and Claude Code measures its 10,000-character limit on what
	// it receives. Shrink the digest until the encoding fits.
	for budget := hookMaxChars; budget > 0; budget = budget * 3 / 4 {
		// Segments are rendered in order against what is left: a file that cannot
		// fit is dropped and its share stays available to the next one.
		remaining := budget - len([]rune(historyPointer))
		var b strings.Builder
		for _, s := range segs {
			d := strings.TrimSuffix(formatFileHistoryCapped(s.path, s.rows, remaining+len([]rune(historyPointer))), historyPointer)
			if d == "" || len([]rune(d)) > remaining {
				continue
			}
			b.WriteString(d)
			remaining -= len([]rune(d))
		}
		if b.Len() == 0 {
			return "" // nothing fits: stay silent rather than send an empty context
		}
		out := encodeHookEnvelope(b.String() + historyPointer)
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
// not a shell: a false candidate costs one DB lookup, a miss costs nothing but
// the context hint. Relative words resolve against the payload cwd, which is the
// session's directory; Claude Code's Bash tool keeps its working directory
// across calls, so after an earlier `cd` the real cwd is unknowable here — a
// command containing cd/pushd therefore contributes absolute paths only. Heredoc
// bodies, comments and redirect targets are not reads of the named file and are
// skipped; a path must stay under cwd after symlinks are resolved.
// debt: 沒做 $VAR／~／glob／命令替換展開；若使用者常以 $PWD/x 或 glob 讀檔就要補
func bashFileCandidates(command, cwd string, max int) []candidateFile {
	if !filepath.IsAbs(cwd) || strings.TrimSpace(command) == "" {
		return nil
	}
	toks, ok := shellwords.Scan(stripHeredocBodies(command))
	if !ok {
		return nil
	}
	cwd = filepath.Clean(cwd)
	realCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil
	}
	cdSeen := false
	for _, t := range toks {
		if !t.Op && (t.Text == "cd" || t.Text == "pushd") {
			cdSeen = true
		}
	}
	var out []candidateFile
	seen := map[string]bool{}
	skipNext := false
	words := 0
	for _, t := range toks {
		if t.Op {
			// A redirect operator consumes the next word as its target (`<<` and
			// `<<<` introduce a heredoc/here-string, not a file to read).
			skipNext = strings.ContainsAny(t.Text, "<>")
			continue
		}
		if words++; words > bashMaxTokens {
			break
		}
		if skipNext {
			skipNext = false
			continue
		}
		tok := strings.Trim(t.Text, ",:")
		if tok == "" || tok[0] == '-' || tok[0] == '$' {
			continue
		}
		if !filepath.IsAbs(tok) {
			if cdSeen {
				continue
			}
			tok = filepath.Join(cwd, tok)
		}
		tok = filepath.Clean(tok)
		if seen[tok] || !pathUnder(cwd, tok) {
			continue
		}
		seen[tok] = true
		real, err := filepath.EvalSymlinks(tok)
		if err != nil || !pathUnder(realCwd, real) {
			continue
		}
		if fi, err := os.Stat(real); err == nil && fi.Mode().IsRegular() {
			out = append(out, candidateFile{tok, fi})
			if len(out) == max {
				break
			}
		}
	}
	return out
}

// heredocStart finds `<<WORD`, `<<-WORD`, `<<'WORD'` or `<<"WORD"` (not `<<<`).
var heredocStart = regexp.MustCompile(`(?:^|[^<])<<(-?)\s*(?:'([^']+)'|"([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))`)

// stripHeredocBodies removes heredoc bodies (the lines after a `<<WORD` line up
// to the terminator line) so text that is only data never yields a candidate and
// stray quotes inside it cannot unbalance the scan. The command line itself is
// kept. A heredoc marker inside a quoted string over-strips; that only drops
// candidates.
func stripHeredocBodies(command string) string {
	lines := strings.Split(command, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		for _, m := range heredocStart.FindAllStringSubmatch(lines[i], -1) {
			word := m[2] + m[3] + m[4]
			for i++; i < len(lines); i++ {
				l := lines[i]
				if m[1] == "-" {
					l = strings.TrimLeft(l, "\t")
				}
				if l == word {
					break
				}
			}
		}
	}
	return strings.Join(out, "\n")
}

// pathUnder reports whether path is strictly inside root, by path elements
// (so /p2 is not under /p).
func pathUnder(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

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
	"github.com/linhn0617/clio/internal/termsafe"
)

func newRecallCmd() *cobra.Command {
	var (
		project    string
		since      string
		limit      int
		tailRunes  int
		noCommands bool
	)
	cmd := &cobra.Command{
		Use:   "recall",
		Short: "Print a recent-activity digest for the current project (used by the SessionStart hook)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Never break session startup: any failure yields empty output, exit 0.
			hook := parseHookPayload(readStdinIfPiped())
			if out := recallDigest(hook, project, since, limit, tailRunes, noCommands); out != "" {
				fmt.Fprint(os.Stdout, out)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project path (matches it and anything beneath; default: detected from the working directory)")
	cmd.Flags().StringVar(&since, "since", "14d", "Look back this far for touched files and run commands")
	cmd.Flags().IntVar(&limit, "limit", 5, "Max items per section")
	cmd.Flags().IntVar(&tailRunes, "tail-runes", 600, "Max length of the last session's closing message excerpt (0 omits it)")
	cmd.Flags().BoolVar(&noCommands, "no-commands", false, "Omit the recent-commands section")
	return cmd
}

// recallDigest builds the digest text, swallowing every error (returns "" on any
// problem) so the SessionStart hook can never break Claude Code startup. hook is
// the SessionStart payload (zero for a manual run): its session is the one being
// opened, so it is excluded from the digest.
func recallDigest(hook recallHook, project, since string, limit, tailRunes int, noCommands bool) string {
	if project == "" {
		project = detectProject(hook.Cwd)
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
	r, err := sessions.GetRecall(context.Background(), database, project, hook.SessionID, sinceTS, limit, limit)
	if err != nil {
		return ""
	}
	if noCommands {
		r.Commands = nil
	}
	applyTailBound(&r, tailRunes)
	return formatRecall(project, r)
}

// applyTailBound collapses the closing-message excerpt to one line and cuts it
// at runes runes (with an ellipsis); 0 drops the section entirely.
func applyTailBound(r *sessions.Recall, runes int) {
	if r.LastAssistant == nil {
		return
	}
	if runes <= 0 {
		r.LastAssistant = nil
		return
	}
	text := strings.Join(strings.Fields(r.LastAssistant.Text), " ")
	if text == "" {
		r.LastAssistant = nil // nothing to show: keep the no-history silence intact
		return
	}
	if rs := []rune(text); len(rs) > runes {
		text = string(rs[:runes]) + "…"
	}
	r.LastAssistant.Text = text
}

// detectProject resolves the current project from the hook payload's `cwd`; a
// manual run (no payload) falls back to the working directory.
func detectProject(cwd string) string {
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

// recallHook is the subset of the SessionStart stdin JSON recall reads.
type recallHook struct {
	Cwd       string `json:"cwd"`
	SessionID string `json:"session_id"`
}

// parseHookPayload decodes a SessionStart payload; empty or garbage input
// yields the zero value (a manual run).
func parseHookPayload(data []byte) recallHook {
	var h recallHook
	if len(data) == 0 || json.Unmarshal(data, &h) != nil {
		return recallHook{}
	}
	return h
}

// parseHookCwd extracts `cwd` from a Claude Code hook JSON payload.
func parseHookCwd(data []byte) string {
	return parseHookPayload(data).Cwd
}

// recallMaxChars caps the digest below Claude Code's 10,000-character hook-output
// limit, past which it injects only a file path and a 2,000-character preview.
// Counted in runes like hookMaxChars: Claude Code speaks of characters, and a
// byte count would cut CJK text to a third of the room.
// debt: runes, not UTF-16 units; an astral character (emoji) counts 2 toward
// Claude Code's limit, so revisit if digests heavy in emoji approach the cap.
const recallMaxChars = 9500

// recallExcerptFloor is how far the closing excerpt is cut when the digest is
// still too long with one session left.
const recallExcerptFloor = 200

// formatRecall renders the digest within recallMaxChars, or "" when there is
// nothing to recall.
func formatRecall(project string, r sessions.Recall) string {
	return formatRecallCapped(project, r, recallMaxChars)
}

// formatRecallCapped shrinks the digest until it fits maxChars runes, dropping
// the least "where did we leave off" material first: the commands section, then
// the files section, then all sessions but the newest, then the excerpt past
// recallExcerptFloor runes; a digest still too long is hard-cut with "…".
func formatRecallCapped(project string, r sessions.Recall, maxChars int) string {
	if r.LastAssistant != nil {
		e := *r.LastAssistant // shrinking below must not touch the caller's excerpt
		r.LastAssistant = &e
	}
	steps := []func(){
		func() { r.Commands = nil },
		func() { r.Files = nil },
		func() {
			if len(r.Sessions) > 1 {
				r.Sessions = r.Sessions[:1]
			}
		},
		func() {
			if e := r.LastAssistant; e != nil {
				if rs := []rune(e.Text); len(rs) > recallExcerptFloor {
					e.Text = string(rs[:recallExcerptFloor]) + "…"
				}
			}
		},
	}
	out := renderRecall(project, r)
	for _, shrink := range steps {
		if len([]rune(out)) <= maxChars {
			return out
		}
		shrink()
		out = renderRecall(project, r)
	}
	if rs := []rune(out); len(rs) > maxChars {
		out = string(rs[:maxChars-1]) + "…"
	}
	return out
}

// renderRecall renders the digest uncapped, or "" when there is nothing to recall.
func renderRecall(project string, r sessions.Recall) string {
	if r.LastAssistant == nil && len(r.Sessions) == 0 && len(r.Files) == 0 && len(r.Commands) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "clio — recent activity in %s\n", termsafe.Strip(project))
	if e := r.LastAssistant; e != nil && e.Text != "" {
		fmt.Fprintf(&b, "Last session left off (%s, %s):\n  %s\n", formatTS(e.TS), shortID(e.SessionUUID), termsafe.Strip(e.Text))
	}
	if len(r.Sessions) > 0 {
		b.WriteString("Recent sessions:\n")
		for _, s := range r.Sessions {
			fmt.Fprintf(&b, "  - %s  %s (%d turns)\n", formatTS(s.EndedAt), oneLine(s.Title, 70), s.TurnCount)
		}
	}
	if len(r.Files) > 0 {
		b.WriteString("Recently touched files:\n")
		for _, f := range r.Files {
			fmt.Fprintf(&b, "  - %s (%dx)\n", termsafe.Strip(f.Value), f.Count)
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

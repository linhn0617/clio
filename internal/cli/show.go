package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/sessions"
)

const defaultShowMessages = 100000

func resolveShowFormat(format string, jsonFlag bool) string {
	if jsonFlag {
		return "json"
	}
	return format
}

func newShowCmd() *cobra.Command {
	var (
		format           string
		noToolOutput     bool
		jsonFlag         bool
		limit            int
		includeSubagents bool
		source           string
	)
	cmd := &cobra.Command{
		Use:   "show <session-uuid-or-prefix>",
		Short: "Print a full session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateSource(source); err != nil {
				return err
			}
			database, err := openAndCatchUp()
			if err != nil {
				return err
			}
			defer database.Close()

			sess, err := sessions.ResolvePrefix(cmd.Context(), database, args[0], source)
			if err != nil {
				return err
			}
			format = resolveShowFormat(format, jsonFlag)
			if limit <= 0 {
				limit = defaultShowMessages
			}
			msgs, _, err := sessions.GetMessages(cmd.Context(), database, sess.UUID, 0, limit, !noToolOutput, true)
			if err != nil {
				return err
			}
			// This session's subagents (its Task-tool children), if any.
			children, err := sessions.ListSessions(cmd.Context(), database, sessions.ListFilter{ParentSession: sess.UUID, Limit: 1000})
			if err != nil {
				return err
			}

			switch format {
			case "json":
				type subagentOut struct {
					sessions.Session
					Messages []jsonMessage `json:"messages,omitempty"`
				}
				rawPruned := anyPruned(msgs)
				subs := make([]subagentOut, 0, len(children))
				for _, c := range children {
					so := subagentOut{Session: c}
					if includeSubagents {
						// includeRaw=true to match the parent's messages and `show <agent-id>`.
						cm, _, err := sessions.GetMessages(cmd.Context(), database, c.UUID, 0, limit, !noToolOutput, true)
						if err != nil {
							return err
						}
						so.Messages = toJSONMessages(cm)
						rawPruned = rawPruned || anyPruned(cm)
					}
					subs = append(subs, so)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Session   sessions.Session `json:"session"`
					Messages  []jsonMessage    `json:"messages"`
					Subagents []subagentOut    `json:"subagents,omitempty"`
					RawPruned bool             `json:"raw_pruned,omitempty"`
				}{sess, toJSONMessages(msgs), subs, rawPruned})
			case "raw":
				sawPruned, err := writeRaw(os.Stdout, msgs)
				if err != nil {
					return err
				}
				if sawPruned {
					prunedNote(shortID(sess.UUID))
				}
				if includeSubagents {
					for _, c := range children {
						cm, _, err := sessions.GetMessages(cmd.Context(), database, c.UUID, 0, limit, !noToolOutput, true)
						if err != nil {
							return err
						}
						sp, err := writeRaw(os.Stdout, cm)
						if err != nil {
							return err
						}
						if sp {
							prunedNote(shortID(c.UUID)) // per-session, not one aggregate note
						}
					}
				}
				return nil
			case "markdown", "":
				fmt.Fprintf(os.Stdout, "# %s\n\n_%s · %s · %d turns_\n\n",
					orPlaceholder(sess.Title, "(untitled session)"), sess.ProjectPath, formatTS(sess.StartedAt), sess.TurnCount)
				if note := subagentNote(sess); note != "" {
					fmt.Fprintf(os.Stdout, "_%s_\n\n", note)
				}
				for _, m := range msgs {
					fmt.Fprintf(os.Stdout, "## %s\n\n%s\n\n", m.Role, m.Content)
				}
				if sec := formatSubagentsSection(children); sec != "" {
					fmt.Fprintf(os.Stdout, "%s\n", sec)
				}
				if includeSubagents {
					for _, c := range children {
						cmsgs, _, err := sessions.GetMessages(cmd.Context(), database, c.UUID, 0, limit, !noToolOutput, true)
						if err != nil {
							return err
						}
						fmt.Fprintf(os.Stdout, "---\n\n# ↳ subagent %s (%s)\n\n", shortID(c.UUID), orPlaceholder(c.AgentType, "subagent"))
						for _, m := range cmsgs {
							fmt.Fprintf(os.Stdout, "## %s\n\n%s\n\n", m.Role, m.Content)
						}
					}
				}
				return nil
			default:
				return fmt.Errorf("invalid --format %q (markdown|json|raw)", format)
			}
		},
	}
	cmd.Flags().StringVar(&format, "format", "markdown", "Output format (markdown|json|raw)")
	cmd.Flags().BoolVar(&noToolOutput, "no-tool-output", false, "Omit tool output")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "Output JSON (alias for --format json)")
	cmd.Flags().IntVar(&limit, "limit", defaultShowMessages, "Maximum messages to render")
	cmd.Flags().BoolVar(&includeSubagents, "include-subagents", false, "Inline the transcripts of this session's subagents")
	addSourceFlag(cmd, &source)
	return cmd
}

// writeRaw prints each message's raw_json, collapsing only runs of consecutive
// identical lines (a session-ingest line expands into adjacent messages that
// share its raw_json). Non-adjacent identical lines are kept distinct. Pruned
// messages (empty raw_json, blanked by `clio prune-raw`) are skipped; sawPruned
// reports whether any were, so the caller can note the pruned raw form.
func writeRaw(w io.Writer, msgs []sessions.Message) (sawPruned bool, err error) {
	have := false
	var last string
	for _, m := range msgs {
		if m.RawJSON == "" {
			sawPruned = true
			continue
		}
		if have && m.RawJSON == last {
			continue
		}
		if _, err := fmt.Fprintln(w, m.RawJSON); err != nil {
			return sawPruned, err
		}
		last, have = m.RawJSON, true
	}
	return sawPruned, nil
}

// prunedNote prints a per-session note (to stderr) that a session's raw form
// was pruned and how to restore it.
func prunedNote(sessionID string) {
	fmt.Fprintf(os.Stderr, "note: raw form of session %s was pruned to save space — restore it with `clio index --full`\n", sessionID)
}

// anyPruned reports whether any message has a blanked (pruned) raw_json.
func anyPruned(msgs []sessions.Message) bool {
	for _, m := range msgs {
		if m.RawJSON == "" {
			return true
		}
	}
	return false
}

// jsonMessage renders a message for --format json with raw_json as null when
// pruned (rather than an empty string), so a pruned raw form is unambiguous.
// Keys match the pre-existing default marshaling of sessions.Message (Go field
// names) so the JSON shape is unchanged except RawJSON becoming nullable.
type jsonMessage struct {
	Seq     int     `json:"Seq"`
	TS      int64   `json:"TS"`
	Role    string  `json:"Role"`
	Content string  `json:"Content"`
	RawJSON *string `json:"RawJSON"`
}

func toJSONMessages(msgs []sessions.Message) []jsonMessage {
	out := make([]jsonMessage, 0, len(msgs))
	for _, m := range msgs {
		jm := jsonMessage{Seq: m.Seq, TS: m.TS, Role: m.Role, Content: m.Content}
		if m.RawJSON != "" {
			rj := m.RawJSON
			jm.RawJSON = &rj
		}
		out = append(out, jm)
	}
	return out
}

func orPlaceholder(s, ph string) string {
	if s == "" {
		return ph
	}
	return s
}

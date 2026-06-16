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
		format       string
		noToolOutput bool
		jsonFlag     bool
		limit        int
	)
	cmd := &cobra.Command{
		Use:   "show <session-uuid-or-prefix>",
		Short: "Print a full session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := openForQuery()
			if err != nil {
				return err
			}
			defer database.Close()

			sess, err := sessions.ResolvePrefix(cmd.Context(), database, args[0])
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

			switch format {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Session  sessions.Session   `json:"session"`
					Messages []sessions.Message `json:"messages"`
				}{sess, msgs})
			case "raw":
				return writeRaw(os.Stdout, msgs)
			case "markdown", "":
				fmt.Fprintf(os.Stdout, "# %s\n\n_%s · %s · %d turns_\n\n",
					orPlaceholder(sess.Title, "(untitled session)"), sess.ProjectPath, formatTS(sess.StartedAt), sess.TurnCount)
				for _, m := range msgs {
					fmt.Fprintf(os.Stdout, "## %s\n\n%s\n\n", m.Role, m.Content)
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
	return cmd
}

// writeRaw prints each message's raw_json, collapsing only runs of consecutive
// identical lines (a session-ingest line expands into adjacent messages that
// share its raw_json). Non-adjacent identical lines are kept distinct.
func writeRaw(w io.Writer, msgs []sessions.Message) error {
	have := false
	var last string
	for _, m := range msgs {
		if have && m.RawJSON == last {
			continue
		}
		if _, err := fmt.Fprintln(w, m.RawJSON); err != nil {
			return err
		}
		last, have = m.RawJSON, true
	}
	return nil
}

func orPlaceholder(s, ph string) string {
	if s == "" {
		return ph
	}
	return s
}

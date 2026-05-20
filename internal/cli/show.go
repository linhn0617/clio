package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/sessions"
)

func newShowCmd() *cobra.Command {
	var (
		format       string
		noToolOutput bool
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

			sess, err := sessions.ResolvePrefix(database, args[0])
			if err != nil {
				return err
			}
			msgs, _, err := sessions.GetMessages(database, sess.UUID, 0, 100000, !noToolOutput)
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
				for _, m := range msgs {
					fmt.Fprintln(os.Stdout, m.RawJSON)
				}
				return nil
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
	return cmd
}

func orPlaceholder(s, ph string) string {
	if s == "" {
		return ph
	}
	return s
}

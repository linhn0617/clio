package cli

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/tui"
)

func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Browse and search history in an interactive dashboard",
		Long: "Launch a full-screen dashboard over your indexed history: live search, recent " +
			"sessions, an activity overview, and question-answering, each with a session preview " +
			"pane. Read-only; switch tabs with Tab or 1-4 and quit with q or Ctrl-C.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := openForQuery()
			if err != nil {
				return err
			}
			defer database.Close()

			// Cancel any in-flight view queries once the UI exits, so the deferred
			// Close doesn't block on a query still running on a large index.
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			p := tea.NewProgram(tui.New(ctx, database),
				tea.WithContext(ctx),
				tea.WithAltScreen())
			_, err = p.Run()
			cancel()
			if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
				return err
			}
			return nil
		},
	}
}

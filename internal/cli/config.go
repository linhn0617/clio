package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/timeutil"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or change settings stored in the index",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newConfigRawRetentionCmd())
	return cmd
}

func newConfigRawRetentionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "raw-retention [VALUE]",
		Short: "Show or set how long stored raw_json is kept (e.g. 14d, or off)",
		Long: `With no VALUE, print the raw_json retention window, or "off".

A VALUE such as 14d or 12h keeps raw_json (used only by 'clio show --format
raw|json') only for sessions whose last activity is within that window: every
ingest stores older sessions without it, and 'clio index' blanks sessions that
aged past it, with the same restorability checks as 'clio prune-raw'. While
'clio mcp' is running, 'clio index' does nothing; the MCP leader runs the same
sweep once when it starts. The window rolls, so a date is rejected. "off"
removes the setting.

To get the raw form back: 'clio config raw-retention off', then
'clio index --full'. The file shrinks on disk only with
'clio prune-raw --vacuum' (or a VACUUM).`,
		Args: cobra.MaximumNArgs(1),
		// "-5d" must reach ParseRetention and get its wording, not cobra's
		// "unknown shorthand flag"; the command has no flags besides help.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				if _, err := os.Stat(dbPath); err != nil {
					fmt.Fprintln(out, "off") // no index yet: nothing is set
					return nil
				}
			}
			var value string
			if len(args) == 1 {
				value = strings.TrimSpace(args[0])
				if strings.EqualFold(value, "off") {
					value = "off"
				} else if _, err := timeutil.ParseRetention(value); err != nil {
					return fmt.Errorf(`%w; use "off" to turn raw retention off`, err)
				}
			}
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer database.Close()
			ctx := cmd.Context()
			switch {
			case len(args) == 0:
				_, stored, err := database.RawRetention(ctx)
				switch {
				case err != nil && stored != "":
					fmt.Fprintf(out, "%s (invalid, treated as off: %v)\n", stored, err)
				case err != nil:
					return err
				case stored == "":
					fmt.Fprintln(out, "off")
				default:
					fmt.Fprintln(out, stored)
				}
				return nil
			case value == "off":
				if err := database.DeleteSetting(ctx, db.RawRetentionKey); err != nil {
					return err
				}
				fmt.Fprintln(out, "raw retention: off — run `clio index --full` to restore raw_json that retention removed")
				return nil
			default:
				if err := database.PutSetting(ctx, db.RawRetentionKey, value); err != nil {
					return err
				}
				fmt.Fprintf(out, "raw retention: %s — raw_json of sessions idle longer than this is not kept; existing sessions are blanked by the next `clio index`, or, while clio mcp is running, when its leader next starts\n", value)
				return nil
			}
		},
	}
}

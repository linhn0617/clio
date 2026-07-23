package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/sessions"
)

func newUsageCmd() *cobra.Command {
	var (
		by       string
		since    string
		project  string
		limit    int
		asJSON   bool
		source   string
		quota    bool
		modelFlt string
	)
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Token usage by session, project, or model (per source; no cross-source totals)",
		Long: `Summarize indexed token usage, sectioned per source. Token counts from
different tools use different tokenizers and are NOT comparable, so no
cross-source grand total is ever printed. Only --by session rows can be opened
directly (clio show <uuid>); project/model rows show a drill-down instead.
--quota prints last-observed quota snapshots from session files — never live.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateSource(source); err != nil {
				return err
			}
			database, err := openAndCatchUp()
			if err != nil {
				return err
			}
			defer database.Close()

			if quota {
				return printQuota(cmd, database, asJSON)
			}

			sinceTS, err := parseSince(since)
			if err != nil {
				return err
			}
			// Subtotals honor the SAME filters as the listing (incl. --model),
			// so a filtered view never shows an unrelated all-model subtotal.
			totalsModel := ""
			if by == "session" {
				totalsModel = modelFlt
			}
			totals, err := sessions.UsageSourceTotals(cmd.Context(), database, sinceTS, project, source, totalsModel)
			if err != nil {
				return err
			}
			if len(totals) == 0 {
				if modelFlt != "" || project != "" || since != "" {
					fmt.Fprintln(os.Stdout, "no usage data matches these filters")
					return nil
				}
				fmt.Fprintln(os.Stdout, "no usage data — usage for sessions indexed before this feature requires a full re-index: run `clio index --full`")
				return nil
			}

			switch by {
			case "session":
				rows, err := sessions.UsageBySession(cmd.Context(), database, sinceTS, project, source, modelFlt, limit)
				if err != nil {
					return err
				}
				if asJSON {
					return jsonOut(map[string]any{"totals": totals, "sessions": rows})
				}
				bySource := map[string][]sessions.UsageSessionRow{}
				for _, r := range rows {
					bySource[r.Source] = append(bySource[r.Source], r)
				}
				for _, t := range totals {
					printSourceHeader(t)
					for _, r := range bySource[t.Source] {
						fmt.Fprintln(os.Stdout, formatUsageSessionRow(r))
					}
					fmt.Fprintln(os.Stdout)
				}
			case "project", "model":
				rows, err := sessions.UsageGrouped(cmd.Context(), database, by, sinceTS, project, source, limit)
				if err != nil {
					return err
				}
				if asJSON {
					return jsonOut(map[string]any{"totals": totals, "groups": rows})
				}
				bySource := map[string][]sessions.UsageGroupRow{}
				for _, r := range rows {
					bySource[r.Source] = append(bySource[r.Source], r)
				}
				for _, t := range totals {
					printSourceHeader(t)
					for _, r := range bySource[t.Source] {
						fmt.Fprintln(os.Stdout, formatUsageGroupRow(by, r))
					}
					fmt.Fprintln(os.Stdout)
				}
			default:
				return fmt.Errorf("--by must be one of session|project|model")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&by, "by", "session", "Group by: session|project|model")
	cmd.Flags().StringVar(&since, "since", "", "Only sessions active since this time (e.g. 7d)")
	cmd.Flags().StringVar(&project, "project", "", "Filter by project path prefix")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum rows per source")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	cmd.Flags().BoolVar(&quota, "quota", false, "Show last-observed quota snapshots (never live)")
	cmd.Flags().StringVar(&modelFlt, "model", "", "Only sessions with usage attributed to this model (with --by session)")
	addSourceFlag(cmd, &source)
	return cmd
}

// formatUsageSessionRow renders one session row: jump-through id, per-category
// totals (spec: rows carry per-category totals, not just the grand total),
// subagent flag, stale marker.
func formatUsageSessionRow(r sessions.UsageSessionRow) string {
	flag := ""
	if r.AgentType != "" {
		flag = "  ↳" + r.AgentType
	}
	stale := ""
	if r.Stale {
		stale = "  [stale]"
	}
	cats := fmt.Sprintf("in %s · out %s · cr %s · cc %s", humanTokens(r.InputTokens), humanTokens(r.OutputTokens), humanTokens(r.CacheRead), humanTokens(r.CacheCreation))
	if r.Reasoning > 0 {
		cats += " · rsn " + humanTokens(r.Reasoning)
	}
	if r.Tool > 0 {
		cats += " · tool " + humanTokens(r.Tool)
	}
	return fmt.Sprintf("  %s  %9s  (%s)  %s  %s%s%s",
		shortID(r.SessionUUID), humanTokens(r.TotalTokens), cats, trimProject(r.ProjectPath), oneLine(r.Title, 50), flag, stale)
}

// formatUsageGroupRow renders one project/model aggregate row, carrying its
// OWN drill-down invocation (aggregates have no single session to jump to).
func formatUsageGroupRow(by string, r sessions.UsageGroupRow) string {
	stale := ""
	if r.StaleSessions > 0 {
		stale = fmt.Sprintf("  [stale: %d sessions]", r.StaleSessions)
	}
	key := r.Key
	drill := ""
	switch by {
	case "project":
		key = trimProject(key)
		drill = fmt.Sprintf("  → clio usage --project %s --by session --source %s", shellQuote(r.Key), r.Source)
	case "model":
		drill = fmt.Sprintf("  → clio usage --model %s --by session --source %s", shellQuote(r.Key), r.Source)
	}
	return fmt.Sprintf("  %9s  %4d sessions  %s%s%s", humanTokens(r.TotalTokens), r.Sessions, key, stale, drill)
}

// shellQuote renders s as a POSIX single-quoted word safe to copy-paste into a
// shell: substitution ($(), backticks) does NOT run inside single quotes,
// unlike Go %q's double quotes. Embedded single quotes become '\”.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func jsonOut(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printSourceHeader(t sessions.UsageSourceTotal) {
	stale := ""
	if t.StaleSessions > 0 {
		stale = fmt.Sprintf("  [stale: %d sessions]", t.StaleSessions)
	}
	fmt.Fprintf(os.Stdout, "%s — %s tokens across %d sessions%s\n", t.Source, humanTokens(t.TotalTokens), t.Sessions, stale)
}

// printQuota renders stored quota snapshots with mandatory staleness: the
// observation age always shows, and a snapshot older than its own window (or
// whose reset time has passed) renders as STALE. These are last-observed
// values from session files, not live readings — and they are CLI-only (never
// exposed over MCP).
func printQuota(cmd *cobra.Command, database *db.DB, asJSON bool) error {
	snaps, err := sessions.QuotaSnapshots(cmd.Context(), database)
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		fmt.Fprintln(os.Stdout, "no quota snapshots — only some tools persist rate-limit state in session files (Codex does)")
		return nil
	}
	now := time.Now()
	if asJSON {
		type snapJSON struct {
			sessions.QuotaSnapshotRow
			ObservedAgo string `json:"observed_ago"`
			Stale       bool   `json:"stale"`
		}
		out := make([]snapJSON, 0, len(snaps))
		for _, s := range snaps {
			out = append(out, snapJSON{s, humanAge(s.ObservedAt, now), quotaStale(s, now)})
		}
		return jsonOut(map[string]any{
			"disclaimer": "last-observed values from session files, not live readings",
			"snapshots":  out,
		})
	}
	fmt.Fprintln(os.Stdout, "last-observed quota snapshots from session files — NOT live readings:")
	for _, s := range snaps {
		state := ""
		if quotaStale(s, now) {
			state = "  STALE"
		}
		reset := ""
		if s.ResetsAt > 0 {
			if resetT := time.Unix(s.ResetsAt, 0); resetT.After(now) {
				reset = fmt.Sprintf(", resets in %s", humanDur(resetT.Sub(now)))
			} else {
				reset = ", reset time passed"
			}
		}
		plan := ""
		if s.PlanType != "" {
			plan = " (" + s.PlanType + ")"
		}
		fmt.Fprintf(os.Stdout, "  %s %s%s: %.1f%% used — observed %s%s%s\n",
			s.Source, s.LimitID, plan, s.UsedPercent, humanAge(s.ObservedAt, now), reset, state)
	}
	return nil
}

// quotaStale: older than its own window, or reset time already passed.
func quotaStale(s sessions.QuotaSnapshotRow, now time.Time) bool {
	if s.WindowMinutes > 0 && now.Unix()-s.ObservedAt > s.WindowMinutes*60 {
		return true
	}
	if s.ResetsAt > 0 && s.ResetsAt < now.Unix() {
		return true
	}
	return false
}

func humanDur(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// usageSuffix annotates a session listing line with its token total ("  [1.2M
// tok]"), a stale marker when the last usage scan failed, and NOTHING when no
// usage data exists (placeholder-by-omission, never a zero).
func usageSuffix(totals map[string]int64, stale map[string]bool, uuid string) string {
	t, ok := totals[uuid]
	if !ok {
		return ""
	}
	s := fmt.Sprintf("  [%s tok", humanTokens(t))
	if stale[uuid] {
		s += ", stale"
	}
	return s + "]"
}

func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// humanAge renders a duration since ts ("3h ago", "2d ago").
func humanAge(ts int64, now time.Time) string {
	d := now.Sub(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

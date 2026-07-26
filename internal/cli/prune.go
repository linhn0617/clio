package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
	"github.com/linhn0617/clio/internal/sessions"
	"github.com/linhn0617/clio/internal/timeutil"
)

func newPruneRawCmd() *cobra.Command {
	var (
		olderThan string
		dryRun    bool
		doVacuum  bool
		source    string
		project   string
	)
	cmd := &cobra.Command{
		Use:   "prune-raw",
		Args:  cobra.NoArgs,
		Short: "Reclaim space by blanking stored raw_json for old, restorable sessions",
		Long: `Blank messages.raw_json (used only by 'clio show --format raw|json') for
sessions older than --older-than whose raw form is restorable from the source
file. This is a reversible cache eviction: 'clio index --full' re-derives the
raw form. Search, content, usage, and activity data are untouched. Space is
reclaimed on disk only with --vacuum (or a later VACUUM).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateSource(source); err != nil {
				return err
			}
			if strings.TrimSpace(olderThan) == "" {
				return fmt.Errorf("--older-than is required (e.g. 14d, 30d, 12h, or YYYY-MM-DD)")
			}
			// Capture now BEFORE parsing so a clock tick between the two cannot let
			// "0d" (cutoff==now) slip past as "strictly past".
			now := time.Now().Unix()
			cutoff, err := timeutil.ParseSince(olderThan)
			if err != nil {
				return err
			}
			// Fail closed: reject zero/negative/future. ParseSince("-5d") yields a
			// FUTURE cutoff and "0d" yields now; either would prune recent/all
			// sessions. Only a strictly-past cutoff is valid.
			if cutoff <= 0 || cutoff >= now {
				return fmt.Errorf("--older-than must be a positive past duration (e.g. 14d), got %q", olderThan)
			}
			return runPruneRaw(cmd.Context(), pruneOpts{
				cutoff: cutoff, dryRun: dryRun, doVacuum: doVacuum, source: source, project: project,
			})
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "", "Prune sessions older than this (14d, 30d, 12h, YYYY-MM-DD) — required")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be pruned; write nothing (takes precedence over --vacuum)")
	cmd.Flags().BoolVar(&doVacuum, "vacuum", false, "VACUUM after pruning to shrink the file on disk")
	addSourceFlag(cmd, &source)
	cmd.Flags().StringVar(&project, "project", "", "Only sessions under this project path prefix")
	return cmd
}

type pruneOpts struct {
	cutoff           int64
	dryRun, doVacuum bool
	source, project  string
}

func runPruneRaw(ctx context.Context, o pruneOpts) error {
	dbPath, err := config.DBPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("no index found at %s — run `clio index` first", dbPath)
	}

	// --vacuum needs exclusive access; check the MCP lock BEFORE any mutation so a
	// refusal is a complete no-op. --dry-run takes precedence and skips this.
	if o.doVacuum && !o.dryRun {
		lockPath, err := config.LockPath()
		if err != nil {
			return fmt.Errorf("cannot determine the index lock path (%w) — refusing to --vacuum; nothing was changed", err)
		}
		if lock.IsHeld(lockPath) {
			return fmt.Errorf("clio mcp holds the index lock — stop it (or the clio MCP server) and re-run `clio prune-raw --vacuum`; nothing was changed")
		}
	}

	database, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer database.Close()

	// Scanned roots for the "restorable / discoverable" check: the Claude
	// projects dir plus every registered source's extra roots.
	var roots []string
	if p, err := config.ClaudeProjectsDir(); err == nil {
		roots = append(roots, p)
	}
	roots = append(roots, ingest.NewWithBuiltinSources(database, discardLogger()).ExtraRoots()...)

	candidates, err := sessions.PruneCandidates(ctx, database, o.cutoff, o.source, o.project)
	if err != nil {
		return err
	}
	pending, err := sessions.PendingBackfillSessions(ctx, database)
	if err != nil {
		return err
	}

	var eligible []sessions.PrunePair
	skips := map[string]int{} // reason -> count
	for _, c := range candidates {
		if reason := classifyPrunable(c, roots, pending); reason != "" {
			skips[reason]++
			continue
		}
		eligible = append(eligible, sessions.PrunePair{UUID: c.UUID, SourceFile: c.SourceFile})
	}

	// PruneRawJSON re-validates eligibility inside the write transaction and
	// returns AUTHORITATIVE counts (validated vs newly-pruned vs raced-out).
	pr, err := sessions.PruneRawJSON(ctx, database, o.cutoff, o.source, o.project, eligible, o.dryRun)
	if err != nil {
		return err
	}
	printPruneSummary(o, len(candidates), pr.PrunedSessions, pr.Messages, pr.RemovedBytes, skips)
	// RacedOut = preflight-eligible sessions the in-transaction revalidation
	// rejected (a concurrent change moved/aged/rescoped them) — distinct from an
	// idempotent rerun (validated but already empty). Reported in real AND dry runs.
	if pr.RacedOut > 0 {
		fmt.Fprintf(os.Stdout, "note: %d eligible session(s) were re-validated out by a concurrent change and left intact.\n", pr.RacedOut)
	}

	if o.dryRun || !o.doVacuum || pr.Messages == 0 {
		return nil
	}
	fmt.Fprintln(os.Stdout, "running VACUUM…")
	if _, err := database.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("prune committed, but VACUUM failed: %w", err)
	}
	fmt.Fprintln(os.Stdout, "VACUUM done — file shrunk on disk.")
	return nil
}

// classifyPrunable returns "" when the candidate is prunable, else a skip reason.
// The DB-side facts (aborted, offset) and filesystem facts (regular/readable/
// under-root/size+mtime match) together establish restorability; pending
// backfill is a separate read-only exclusion.
func classifyPrunable(c sessions.PruneCandidate, roots []string, pending map[string]bool) string {
	if c.Aborted != 0 {
		return "unverified"
	}
	if c.LastOffset != c.LastSize {
		return "lagged"
	}
	if pending[c.UUID] {
		return "pending-backfill"
	}
	fi, err := os.Stat(c.SourceFile)
	if err != nil {
		return "missing"
	}
	if !fi.Mode().IsRegular() {
		return "unreadable"
	}
	if !underAnyRoot(c.SourceFile, roots) {
		return "undiscoverable"
	}
	// The indexed snapshot must still be the file on disk, else a forced re-parse
	// could yield different messages and leave the pruned ones unrestored.
	if fi.Size() != c.LastSize || fi.ModTime().UnixNano() != c.LastMtime {
		return "changed"
	}
	f, err := os.Open(c.SourceFile)
	if err != nil {
		return "unreadable"
	}
	f.Close()
	return ""
}

func underAnyRoot(path string, roots []string) bool {
	for _, r := range roots {
		if r == "" {
			continue
		}
		rel, err := filepath.Rel(r, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func printPruneSummary(o pruneOpts, candidates, eligible int, msgs, removed int64, skips map[string]int) {
	verb := "pruned"
	if o.dryRun {
		verb = "would prune"
	}
	fmt.Fprintf(os.Stdout, "%s %d messages across %d sessions (%s = %d bytes of raw_json)\n",
		verb, msgs, eligible, humanBytes(removed), removed)
	if len(skips) > 0 {
		reasons := make([]string, 0, len(skips))
		total := 0
		for r, n := range skips {
			reasons = append(reasons, r)
			total += n // authoritative: sum of preflight skip reasons, NOT
			// candidates-pruned (which would miscount idempotent already-empty sessions)
		}
		sort.Strings(reasons)
		parts := make([]string, 0, len(reasons))
		for _, r := range reasons {
			parts = append(parts, fmt.Sprintf("%s %d", r, skips[r]))
		}
		fmt.Fprintf(os.Stdout, "skipped %d of %d candidate sessions: %s\n",
			total, candidates, strings.Join(parts, ", "))
	}
	if o.dryRun {
		return
	}
	if !o.doVacuum && msgs > 0 {
		fmt.Fprintln(os.Stdout, "space is freed in the DB but the file shrinks only on VACUUM — re-run with --vacuum, or run VACUUM manually.")
	}
	if msgs > 0 {
		fmt.Fprintln(os.Stdout, "restore the raw form anytime with `clio index --full`.")
	}
}

// humanBytes renders a byte count compactly (MB/kB).
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

package sessions

import (
	"context"
	"strings"

	"github.com/linhn0617/clio/internal/db"
)

// PruneCandidate is a session old enough to consider pruning, with the stored
// ingest snapshot fields the caller checks for restorability against the file
// on disk. Filesystem facts (regular/readable/under-root/size-mtime match) are
// checked by the caller; the DB-side facts (offset, aborted) are here.
type PruneCandidate struct {
	UUID       string
	Source     string
	SourceFile string
	LastSize   int64
	LastMtime  int64
	LastOffset int64
	Aborted    int64
}

// PruneCandidates returns sessions whose most recent activity is strictly older
// than cutoff, LEFT-joined with their ingest_state snapshot (a session with no
// state row surfaces with aborted=1 / offsets -1 so the caller skips it as
// unverified). source ("" / "all" / a name) and project (path prefix) filter as
// elsewhere. Restorability (offset/aborted + filesystem) is judged by the caller.
func PruneCandidates(ctx context.Context, database *db.DB, cutoff int64, source, project string) ([]PruneCandidate, error) {
	where := " WHERE COALESCE(s.ended_at, s.started_at, 0) < ?"
	args := []any{cutoff}
	if project != "" {
		where += ` AND s.project_path LIKE ? ESCAPE '\'`
		args = append(args, db.EscapeLike(project)+"%")
	}
	if source != "" && source != "all" {
		where += " AND COALESCE(s.source,'claude-code') = ?"
		args = append(args, source)
	}
	q := `SELECT s.uuid, COALESCE(s.source,'claude-code'), s.source_file,
		COALESCE(i.last_size,-1), COALESCE(i.last_mtime,-1), COALESCE(i.last_byte_offset,-1), COALESCE(i.aborted,1)
		FROM sessions s LEFT JOIN ingest_state i ON i.source_file = s.source_file` + where
	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PruneCandidate
	for rows.Next() {
		var c PruneCandidate
		if err := rows.Scan(&c.UUID, &c.Source, &c.SourceFile, &c.LastSize, &c.LastMtime, &c.LastOffset, &c.Aborted); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PendingBackfillSessions returns the set of claude-code session uuids that
// still have a tool_use message without extracted tool_targets — i.e. a pending
// activity backfill that would need their raw_json. prune-raw excludes these
// (read-only; it never runs the backfill itself).
func PendingBackfillSessions(ctx context.Context, database *db.DB) (map[string]bool, error) {
	rows, err := database.QueryContext(ctx, `SELECT DISTINCT m.session_uuid
		FROM messages m JOIN sessions s ON s.uuid = m.session_uuid
		WHERE m.role = 'tool_use' AND COALESCE(s.source,'claude-code') = 'claude-code'
		  AND NOT EXISTS(SELECT 1 FROM tool_targets tt WHERE tt.message_id = m.id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out[u] = true
	}
	return out, rows.Err()
}

// PrunePair binds a session uuid to the exact source_file the caller
// filesystem-validated, so the transaction can reject a session whose
// source_file changed (e.g. a concurrent full ingest moved it) since preflight.
type PrunePair struct {
	UUID       string
	SourceFile string
}

// PruneResult reports a prune's authoritative counts. ValidatedSessions is the
// number of preflight-eligible sessions that ALSO passed the in-transaction
// revalidation (age/source/project/aborted/offset/pending + exact
// (uuid,source_file) tuple). PrunedSessions/Messages/RemovedBytes cover only the
// rows actually blanked (raw_json<>”); an already-empty (idempotent rerun)
// session is validated but not newly-pruned. RacedOut = preflight-eligible minus
// validated = sessions a concurrent change revalidated out.
type PruneResult struct {
	ValidatedSessions int
	PrunedSessions    int
	Messages          int64
	RemovedBytes      int64
	RacedOut          int
}

// PruneRawJSON blanks raw_json (to ”) for the messages of the given eligible
// sessions, in a single write transaction. Eligibility is re-validated inside
// the transaction via a TEMP table (age/source/project/aborted/offset/pending +
// exact (uuid,source_file) tuple), so it cannot be raced by an interleaved
// ingest and does not blow SQLite's host-parameter limit on large histories.
// With dryRun it computes the counts and writes nothing.
func PruneRawJSON(ctx context.Context, database *db.DB, cutoff int64, source, project string, pairs []PrunePair, dryRun bool) (PruneResult, error) {
	var res PruneResult
	if len(pairs) == 0 {
		return res, nil
	}
	tx, err := database.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	// Preflight candidate pairs into a TEMP table (bounded batches — no unbounded
	// host-parameter list). TEMP tables are connection-scoped; this tx is one conn.
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS _prune_cand(uuid TEXT PRIMARY KEY, source_file TEXT)`); err != nil {
		return res, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM _prune_cand`); err != nil {
		return res, err
	}
	const batch = 400
	for i := 0; i < len(pairs); i += batch {
		end := min(i+batch, len(pairs))
		vals := "(?,?)" + strings.Repeat(",(?,?)", end-i-1)
		args := make([]any, 0, 2*(end-i))
		for _, p := range pairs[i:end] {
			args = append(args, p.UUID, p.SourceFile)
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO _prune_cand(uuid, source_file) VALUES `+vals, args...); err != nil {
			return res, err
		}
	}

	// eligible: candidate rows whose (uuid, source_file) still matches sessions and
	// which pass every DB-side predicate. This is the authoritative validated set.
	elig := `SELECT c.uuid FROM _prune_cand c
		JOIN sessions s ON s.uuid = c.uuid AND s.source_file = c.source_file
		JOIN ingest_state i ON i.source_file = s.source_file
		WHERE COALESCE(s.ended_at, s.started_at, 0) < ?
		  AND i.aborted = 0 AND i.last_byte_offset = i.last_size
		  AND NOT EXISTS(SELECT 1 FROM messages m2 WHERE m2.session_uuid = s.uuid
		      AND m2.role = 'tool_use' AND COALESCE(s.source,'claude-code') = 'claude-code'
		      AND NOT EXISTS(SELECT 1 FROM tool_targets tt WHERE tt.message_id = m2.id))`
	eligArgs := []any{cutoff}
	if source != "" && source != "all" {
		elig += " AND COALESCE(s.source,'claude-code') = ?"
		eligArgs = append(eligArgs, source)
	}
	if project != "" {
		elig += ` AND s.project_path LIKE ? ESCAPE '\'`
		eligArgs = append(eligArgs, db.EscapeLike(project)+"%")
	}

	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+elig+`)`, eligArgs...).Scan(&res.ValidatedSessions); err != nil {
		return res, err
	}
	res.RacedOut = len(pairs) - res.ValidatedSessions

	pred := `raw_json <> '' AND session_uuid IN (` + elig + `)`
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT session_uuid), COALESCE(SUM(length(CAST(raw_json AS BLOB))), 0) FROM messages WHERE `+pred,
		eligArgs...).Scan(&res.Messages, &res.PrunedSessions, &res.RemovedBytes); err != nil {
		return res, err
	}
	if dryRun || res.Messages == 0 {
		return res, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET raw_json = '' WHERE `+pred, eligArgs...); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	return res, nil
}

// PrunedResiduals returns the uuids of sessions that still have any pruned
// (empty) raw_json — used by `clio index --full`'s post-run check to fail loudly
// on an unrestored prune.
func PrunedResiduals(ctx context.Context, database *db.DB) ([]string, error) {
	rows, err := database.QueryContext(ctx, `SELECT DISTINCT session_uuid FROM messages WHERE raw_json = ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

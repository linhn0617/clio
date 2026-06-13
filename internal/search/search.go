package search

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/linhn0617/clio/internal/db"
)

// overscan: fetch Limit*overscan rows by bm25 before re-ranking by recency/role, so a
// recency-boosted hit ranked just outside the top-N by bm25 still survives re-ranking.
const overscan = 12

// Search runs a query and returns ranked results.
func Search(ctx context.Context, database *db.DB, opt Options) ([]Result, error) {
	if strings.TrimSpace(opt.Query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	if opt.Limit <= 0 {
		opt.Limit = 20
	}

	ts := terms(opt.Query)
	if len(ts) == 0 {
		// Non-empty input that parses to zero terms (e.g. only quote characters)
		// has nothing searchable; return an empty set rather than building
		// malformed SQL with no content predicate.
		return nil, nil
	}
	long, short := partitionTerms(ts)

	var (
		rows *sql.Rows
		err  error
	)
	if len(long) > 0 {
		rows, err = hybridQuery(ctx, database, opt, long, short)
	} else {
		rows, err = likeQuery(ctx, database, opt)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var r Result
		var bm float64
		if err := rows.Scan(&r.MessageID, &r.SessionUUID, &r.ProjectPath, &r.Role, &r.TS, &r.Snippet, &bm); err != nil {
			return nil, err
		}
		r.Score = adjustedScore(bm, r.Role, r.TS)
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > opt.Limit {
		results = results[:opt.Limit]
	}
	return results, nil
}

func roleFilter(opt Options) (string, []any) {
	if opt.Role != "" {
		return " AND m.role = ?", []any{opt.Role}
	}
	if !opt.IncludeToolOutput {
		return " AND m.role IN ('user','assistant')", nil
	}
	return "", nil
}

func commonFilters(opt Options) (string, []any) {
	var sb strings.Builder
	var args []any
	if rf, ra := roleFilter(opt); rf != "" {
		sb.WriteString(rf)
		args = append(args, ra...)
	}
	if opt.Since > 0 {
		sb.WriteString(" AND m.ts >= ?")
		args = append(args, opt.Since)
	}
	if opt.ProjectPrefix != "" {
		sb.WriteString(` AND s.project_path LIKE ? ESCAPE '\'`)
		args = append(args, db.EscapeLike(opt.ProjectPrefix)+"%")
	}
	if opt.Touched != "" {
		sb.WriteString(` AND m.session_uuid IN (SELECT session_uuid FROM tool_targets WHERE kind='file' AND value LIKE ? ESCAPE '\')`)
		args = append(args, db.EscapeLike(opt.Touched)+"%")
	}
	if opt.Tool != "" {
		sb.WriteString(` AND m.session_uuid IN (SELECT session_uuid FROM tool_targets WHERE kind='tool' AND value = ?)`)
		args = append(args, opt.Tool)
	}
	if opt.Ran != "" {
		sb.WriteString(` AND m.session_uuid IN (SELECT session_uuid FROM tool_targets WHERE kind='command' AND value LIKE ? ESCAPE '\')`)
		args = append(args, "%"+db.EscapeLike(opt.Ran)+"%")
	}
	return sb.String(), args
}

// hybridQuery uses FTS MATCH for long terms and adds per-short-term LIKE filters.
func hybridQuery(ctx context.Context, database *db.DB, opt Options, long, short []string) (*sql.Rows, error) {
	filt, fargs := commonFilters(opt)

	// Build short-term LIKE clauses.
	var shortClauses strings.Builder
	var shortArgs []any
	for _, t := range short {
		shortClauses.WriteString(` AND m.content LIKE ? ESCAPE '\'`)
		shortArgs = append(shortArgs, "%"+db.EscapeLike(t)+"%")
	}

	matchExpr := buildMatchQuery(long)
	q := `SELECT m.id, m.session_uuid, COALESCE(s.project_path,''), m.role, COALESCE(m.ts,0),
		snippet(messages_fts,0,'[',']','…',10), bm25(messages_fts)
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		LEFT JOIN sessions s ON s.uuid = m.session_uuid
		WHERE messages_fts MATCH ?` + shortClauses.String() + filt + `
		ORDER BY bm25(messages_fts) LIMIT ?`

	args := []any{matchExpr}
	args = append(args, shortArgs...)
	args = append(args, fargs...)
	args = append(args, opt.Limit*overscan)
	return database.QueryContext(ctx, q, args...)
}

func likeQuery(ctx context.Context, database *db.DB, opt Options) (*sql.Rows, error) {
	filt, fargs := commonFilters(opt)
	var conds []string
	var args []any
	for _, t := range terms(opt.Query) {
		conds = append(conds, `m.content LIKE ? ESCAPE '\'`)
		args = append(args, "%"+db.EscapeLike(t)+"%")
	}
	where := strings.Join(conds, " AND ")
	// LIKE has no bm25; emit content as the "snippet" source (trimmed later) and 0 score.
	q := `SELECT m.id, m.session_uuid, COALESCE(s.project_path,''), m.role, COALESCE(m.ts,0),
		substr(m.content,1,160), 0.0
		FROM messages m
		LEFT JOIN sessions s ON s.uuid = m.session_uuid
		WHERE ` + where + filt + `
		ORDER BY m.ts DESC LIMIT ?`
	args = append(args, fargs...)
	args = append(args, opt.Limit*overscan)
	return database.QueryContext(ctx, q, args...)
}

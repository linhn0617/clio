package search

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/linhn0617/clio/internal/db"
)

const overscan = 5 // fetch extra rows so post-ranking can reorder meaningfully

// Search runs a query and returns ranked results.
func Search(database *db.DB, opt Options) ([]Result, error) {
	if strings.TrimSpace(opt.Query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	if opt.Limit <= 0 {
		opt.Limit = 20
	}

	var (
		rows *sql.Rows
		err  error
	)
	if needsLikeFallback(opt.Query) {
		rows, err = likeQuery(database, opt)
	} else {
		rows, err = ftsQuery(database, opt)
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
		sb.WriteString(" AND s.project_path LIKE ?")
		args = append(args, opt.ProjectPrefix+"%")
	}
	return sb.String(), args
}

func ftsQuery(database *db.DB, opt Options) (*sql.Rows, error) {
	filt, fargs := commonFilters(opt)
	q := `SELECT m.id, m.session_uuid, COALESCE(s.project_path,''), m.role, COALESCE(m.ts,0),
		snippet(messages_fts,0,'[',']','…',10), bm25(messages_fts)
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		LEFT JOIN sessions s ON s.uuid = m.session_uuid
		WHERE messages_fts MATCH ?` + filt + `
		ORDER BY bm25(messages_fts) LIMIT ?`
	args := append([]any{opt.Query}, fargs...)
	args = append(args, opt.Limit*overscan)
	return database.Query(q, args...)
}

func likeQuery(database *db.DB, opt Options) (*sql.Rows, error) {
	filt, fargs := commonFilters(opt)
	var conds []string
	var args []any
	for _, t := range terms(opt.Query) {
		conds = append(conds, "m.content LIKE ?")
		args = append(args, "%"+t+"%")
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
	return database.Query(q, args...)
}

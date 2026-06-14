package search

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/linhn0617/clio/internal/db"
)

// Candidate is a retrieval hit plus its in-session sequence, for callers that
// assemble windowed context around it (clio ask).
type Candidate struct {
	SessionUUID string
	ProjectPath string
	Seq         int
	TS          int64
	Role        string
	Snippet     string
	Score       float64
}

// Retrieve runs an any-term (OR) match over the query terms and returns candidate
// hits ranked by adjusted score (bm25 + recency/role). A message matching ANY term
// qualifies — unlike Search, which ANDs all terms — preserving recall for the
// many-term queries a natural-language question produces. Long terms (>=3 runes)
// drive an FTS OR match and short terms an OR of substring LIKEs; both run and
// their hits merge (dedup by session+seq, keeping the higher score), so a short
// discriminator (a 2-rune CJK word, "go", "v2") still contributes to recall in a
// mixed query rather than being dropped.
func Retrieve(ctx context.Context, database *db.DB, opt Options) ([]Candidate, error) {
	if opt.Limit <= 0 {
		opt.Limit = 60
	}
	ts := terms(opt.Query)
	if len(ts) == 0 {
		return nil, nil
	}
	long, short := partitionTerms(ts)

	type key struct {
		sess string
		seq  int
	}
	byKey := map[key]Candidate{}
	scan := func(rows *sql.Rows, err error) error {
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Candidate
			var bm float64
			if err := rows.Scan(&c.SessionUUID, &c.ProjectPath, &c.Seq, &c.TS, &c.Role, &c.Snippet, &bm); err != nil {
				return err
			}
			c.Score = adjustedScore(bm, c.Role, c.TS)
			k := key{c.SessionUUID, c.Seq}
			if prev, ok := byKey[k]; !ok || c.Score > prev.Score {
				byKey[k] = c
			}
		}
		return rows.Err()
	}

	if len(long) > 0 {
		if err := scan(anyMatchQuery(ctx, database, opt, long)); err != nil {
			return nil, err
		}
	}
	if len(short) > 0 {
		if err := scan(anyLikeQuery(ctx, database, opt, short)); err != nil {
			return nil, err
		}
	}

	out := make([]Candidate, 0, len(byKey))
	for _, c := range byKey {
		out = append(out, c)
	}
	// Deterministic order: score desc, then session/seq so ties don't depend on
	// map iteration order.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].SessionUUID != out[j].SessionUUID {
			return out[i].SessionUUID < out[j].SessionUUID
		}
		return out[i].Seq < out[j].Seq
	})
	if len(out) > opt.Limit {
		out = out[:opt.Limit]
	}
	return out, nil
}

// buildAnyMatchQuery turns terms into an operator-safe FTS5 MATCH expression that
// matches ANY term: each term is a quoted phrase (embedded " doubled), joined by
// " OR ". The any-term counterpart to buildMatchQuery's AND join.
func buildAnyMatchQuery(terms []string) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	return strings.Join(parts, " OR ")
}

// anyMatchQuery runs an FTS OR match over the long terms, selecting m.seq for
// windowing.
func anyMatchQuery(ctx context.Context, database *db.DB, opt Options, long []string) (*sql.Rows, error) {
	filt, fargs := commonFilters(opt)
	q := `SELECT m.session_uuid, COALESCE(s.project_path,''), m.seq, COALESCE(m.ts,0), m.role,
		snippet(messages_fts,0,'[',']','…',10), bm25(messages_fts)
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		LEFT JOIN sessions s ON s.uuid = m.session_uuid
		WHERE messages_fts MATCH ?` + filt + `
		ORDER BY bm25(messages_fts) LIMIT ?`
	args := []any{buildAnyMatchQuery(long)}
	args = append(args, fargs...)
	args = append(args, opt.Limit*overscan)
	return database.QueryContext(ctx, q, args...)
}

// anyLikeQuery runs an OR of substring LIKEs over the short terms (the all-short
// fallback, e.g. a question of only 1-2 rune CJK terms), selecting m.seq.
func anyLikeQuery(ctx context.Context, database *db.DB, opt Options, short []string) (*sql.Rows, error) {
	filt, fargs := commonFilters(opt)
	var conds []string
	var args []any
	for _, t := range short {
		conds = append(conds, `m.content LIKE ? ESCAPE '\'`)
		args = append(args, "%"+db.EscapeLike(t)+"%")
	}
	q := `SELECT m.session_uuid, COALESCE(s.project_path,''), m.seq, COALESCE(m.ts,0), m.role,
		substr(m.content,1,160), 0.0
		FROM messages m
		LEFT JOIN sessions s ON s.uuid = m.session_uuid
		WHERE (` + strings.Join(conds, " OR ") + `)` + filt + `
		ORDER BY m.ts DESC LIMIT ?`
	args = append(args, fargs...)
	args = append(args, opt.Limit*overscan)
	return database.QueryContext(ctx, q, args...)
}

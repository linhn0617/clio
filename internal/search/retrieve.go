package search

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/linhn0617/clio/internal/db"
)

// shortAlsoBoost nudges a both-tier hit (matched a long FTS term and a short term)
// up within the FTS tier. Bounded so it can't outweigh a clearly stronger FTS hit.
const shortAlsoBoost = 0.5

// Candidate is a retrieval hit plus its in-session sequence, for callers that
// assemble windowed context around it (clio ask). FTS marks a full-term index
// match (a stronger signal than a short-term substring LIKE), so callers can rank
// FTS hits ahead of LIKE-only hits without comparing the two incompatible score
// scales (bm25 relevance vs. substring match count).
type Candidate struct {
	SessionUUID string
	ProjectPath string
	Seq         int
	TS          int64
	Role        string
	Snippet     string
	Score       float64
	FTS         bool
}

// Retrieve runs an any-term (OR) match over queryTerms and returns candidate hits
// ranked by adjusted score (bm25 + recency/role). A message matching ANY term
// qualifies — unlike Search, which ANDs all terms — preserving recall for the
// many-term queries a natural-language question produces. Long terms (>=3 runes)
// drive an FTS OR match and short terms an OR of substring LIKEs; both run and
// their hits merge (dedup by session+seq, keeping the higher score), so a short
// discriminator (a 2-rune CJK word, "go", "v2") still contributes to recall in a
// mixed query rather than being dropped.
//
// queryTerms is a pre-split term slice, not a query string: callers (ask) already
// extract discrete terms, and joining them into one string for Retrieve to
// re-split via terms() (which honors double quotes) let a term with an internal
// unmatched quote — e.g. a pasted code fragment like `foo("bar` — swallow every
// following term into a single, near-unmatchable FTS phrase. Passing the slice
// directly removes that join-then-reparse seam. opt.Query is unused here (Search
// still parses it via terms()).
func Retrieve(ctx context.Context, database *db.DB, queryTerms []string, opt Options) ([]Candidate, error) {
	if opt.Limit <= 0 {
		opt.Limit = 60
	}
	if len(queryTerms) == 0 {
		return nil, nil
	}
	long, short := partitionTerms(queryTerms)

	type key struct {
		sess string
		seq  int
	}
	byKey := map[key]Candidate{}
	alsoShort := map[key]bool{} // FTS keys that also matched a short term
	scan := func(rows *sql.Rows, err error, fromFTS bool) error {
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
			c.FTS = fromFTS
			k := key{c.SessionUUID, c.Seq}
			prev, ok := byKey[k]
			// A short-term hit on a message already matched by FTS keeps the FTS row,
			// but records that it also matched a short term (boosted below).
			if ok && !fromFTS && prev.FTS {
				alsoShort[k] = true
			}
			// Prefer the FTS tier; within a tier keep the higher score.
			if !ok || (c.FTS && !prev.FTS) || (c.FTS == prev.FTS && c.Score > prev.Score) {
				byKey[k] = c
			}
		}
		return rows.Err()
	}

	if len(long) > 0 {
		rows, qerr := anyMatchQuery(ctx, database, opt, long)
		if err := scan(rows, qerr, true); err != nil {
			return nil, err
		}
	}
	if len(short) > 0 {
		rows, qerr := anyLikeQuery(ctx, database, opt, short)
		if err := scan(rows, qerr, false); err != nil {
			return nil, err
		}
	}

	out := make([]Candidate, 0, len(byKey))
	for k, c := range byKey {
		// A both-tier hit (matched a long FTS term and a short term) is more
		// relevant than a long-only hit; nudge it up within the FTS tier. The boost
		// is bounded so it can't outweigh a clearly stronger FTS match.
		if c.FTS && alsoShort[k] {
			c.Score += shortAlsoBoost
		}
		out = append(out, c)
	}
	// Tier FTS hits ahead of LIKE-only hits (incompatible score scales), then by
	// score within a tier; session/seq make ties deterministic.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FTS != out[j].FTS {
			return out[i].FTS
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].SessionUUID != out[j].SessionUUID {
			return out[i].SessionUUID < out[j].SessionUUID
		}
		return out[i].Seq < out[j].Seq
	})
	// Cap per session (post-rank) so one session repeating the terms across many
	// turns can't fill the pool and starve other relevant sessions out of grouping.
	if opt.MaxPerSession > 0 {
		perSession := make(map[string]int)
		kept := out[:0]
		for _, c := range out {
			if perSession[c.SessionUUID] >= opt.MaxPerSession {
				continue
			}
			perSession[c.SessionUUID]++
			kept = append(kept, c)
		}
		out = kept
	}
	if len(out) > opt.Limit {
		out = out[:opt.Limit]
	}
	return out, nil
}

// buildAnyMatchQuery turns terms into an operator-safe FTS5 MATCH expression that
// matches ANY term: quoted phrases joined by " OR ". The any-term counterpart to
// buildMatchQuery's AND join (both share quotedTerms for escaping).
func buildAnyMatchQuery(terms []string) string {
	return strings.Join(quotedTerms(terms), " OR ")
}

// anyMatchQuery runs an FTS OR match over the long terms, selecting m.seq for
// windowing.
func anyMatchQuery(ctx context.Context, database *db.DB, opt Options, long []string) (*sql.Rows, error) {
	filt, fargs := commonFilters(opt)
	// substr (not snippet()): ask windows the real content via GetWindow and never
	// reads Candidate.Snippet, so the per-row snippet() cost is pure waste here.
	q := `SELECT m.session_uuid, COALESCE(s.project_path,''), m.seq, COALESCE(m.ts,0), m.role,
		substr(m.content,1,160), bm25(messages_fts)
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

// anyLikeQuery matches any short term via substring LIKE and scores each hit by
// how many of the short terms it contains (not recency alone), so a message
// matching more of the question ranks higher. The per-term booleans are summed in
// an inner query; the score is emitted as a negative count so it slots into
// adjustedScore's bm25 flip (more matches -> more negative -> higher relevance).
func anyLikeQuery(ctx context.Context, database *db.DB, opt Options, short []string) (*sql.Rows, error) {
	filt, fargs := commonFilters(opt)
	conds := make([]string, 0, len(short))
	var pat []any
	for _, t := range short {
		conds = append(conds, `(m.content LIKE ? ESCAPE '\')`)
		pat = append(pat, "%"+db.EscapeLike(t)+"%")
	}
	matchCount := strings.Join(conds, " + ")
	orClause := strings.Join(conds, " OR ")
	// The OR predicate is in the inner WHERE so SQLite discards non-matching rows
	// during the scan (instead of computing the match count for every row and
	// filtering after); mc remains only as the relevance score / sort key.
	q := `SELECT session_uuid, project_path, seq, ts, role, snippet, -mc FROM (
		SELECT m.session_uuid AS session_uuid, COALESCE(s.project_path,'') AS project_path,
			m.seq AS seq, COALESCE(m.ts,0) AS ts, m.role AS role,
			substr(m.content,1,160) AS snippet, (` + matchCount + `) AS mc
		FROM messages m
		LEFT JOIN sessions s ON s.uuid = m.session_uuid
		WHERE (` + orClause + `)` + filt + `
	) ORDER BY mc DESC, ts DESC LIMIT ?`
	args := make([]any, 0, len(pat)*2+len(fargs)+1)
	args = append(args, pat...) // matchCount in SELECT
	args = append(args, pat...) // orClause in WHERE
	args = append(args, fargs...)
	args = append(args, opt.Limit*overscan)
	return database.QueryContext(ctx, q, args...)
}

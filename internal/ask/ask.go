package ask

import (
	"context"
	"sort"
	"unicode/utf8"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/search"
	"github.com/linhn0617/clio/internal/sessions"
)

// Defaults applied when an Option is left zero.
const (
	defaultMaxSessions   = 6
	defaultWindow        = 2
	defaultMaxExcerptLen = 600
	maxHitsPerSession    = 3   // bound the windows assembled per session
	minCandidatePool     = 200 // floor on retrieved hits before session grouping
	coverageBonus        = 0.5 // per short-term hit an FTS session also has (bounded)
)

// Options controls a retrieval bundle.
type Options struct {
	Question      string
	ProjectPrefix string // "" = all projects
	Since         int64  // unix seconds; 0 = no lower bound
	MaxSessions   int    // cap on grouped sessions (default 6)
	Window        int    // dialogue turns each side of a hit (default 2)
	MaxExcerptLen int    // per-excerpt rune cap (default 600)
	MaxTokens     int    // global budget over the bundle's excerpt text (default 2000)
	Source        string // "" / "claude-code" (default) | "codex" | "all"
}

// Answer is the evidence bundle for a question: ranked sessions, each with
// windowed, cited excerpts for the caller to synthesize from. clio performs no
// generation.
type Answer struct {
	Question string          `json:"question"`
	Groups   []EvidenceGroup `json:"groups"`
}

// EvidenceGroup is one session's contribution, with a citation.
type EvidenceGroup struct {
	SessionUUID string    `json:"session_uuid"`
	Title       string    `json:"title"`
	Project     string    `json:"project"`
	EndedAt     int64     `json:"ended_at"`
	Score       float64   `json:"score"`
	Source      string    `json:"source"` // originating tool: "claude-code" | "codex"
	Excerpts    []Excerpt `json:"excerpts"`
}

// Excerpt is one message in a window; IsHit marks the ones that matched the query.
type Excerpt struct {
	Seq   int    `json:"seq"`
	TS    int64  `json:"ts"`
	Role  string `json:"role"`
	Text  string `json:"text"`
	IsHit bool   `json:"is_hit"`
}

// Ask builds the evidence bundle for a natural-language question: extract content
// terms, retrieve any-term matches, group by session, window each hit in dialogue
// space, rank sessions by best hit score, and cap to the budget. Retrieval-only —
// no generation, no network.
func Ask(ctx context.Context, database *db.DB, opt Options) (Answer, error) {
	// Non-nil Groups so an empty result serializes as [] (a stable array), not null.
	ans := Answer{Question: opt.Question, Groups: []EvidenceGroup{}}
	if opt.MaxSessions <= 0 {
		opt.MaxSessions = defaultMaxSessions
	}
	if opt.Window <= 0 {
		opt.Window = defaultWindow
	}
	if opt.MaxExcerptLen <= 0 {
		opt.MaxExcerptLen = defaultMaxExcerptLen
	}
	if opt.MaxTokens <= 0 {
		opt.MaxTokens = defaultMaxTokens
	}
	// Clamp caller-supplied budgets here so every surface (CLI, MCP, direct
	// callers) gets the same floor/ceiling regardless of its own validation.
	if opt.MaxTokens < minMaxTokens {
		opt.MaxTokens = minMaxTokens
	}
	if opt.MaxTokens > maxMaxTokens {
		opt.MaxTokens = maxMaxTokens
	}

	terms := extractTerms(opt.Question)
	if len(terms) == 0 {
		return ans, nil
	}

	// A generous candidate pool (not tightly MaxSessions-scaled): grouping collapses
	// these to sessions, and a pool that is too small lets one session repeating the
	// query terms across many turns starve other relevant sessions out of the result.
	pool := max(opt.MaxSessions*40, minCandidatePool)
	// terms is passed directly (not joined into a query string): see Retrieve's doc
	// comment for why joining-then-reparsing breaks on a term with an internal
	// unmatched quote.
	cands, err := search.Retrieve(ctx, database, terms, search.Options{
		Since:         opt.Since,
		ProjectPrefix: opt.ProjectPrefix,
		Limit:         pool,
		MaxPerSession: maxHitsPerSession, // a session only needs this many hits to window + rank
		Source:        opt.Source,
	})
	if err != nil {
		return ans, err
	}
	if len(cands) == 0 {
		return ans, nil
	}

	// Group candidates by session, keeping FTS-hit and LIKE-hit scores apart (their
	// scales are incompatible) along with every matched seq (cands are pre-ranked).
	type group struct {
		ftsScores  []float64
		likeScores []float64
		hitSeqs    []int
		hasFTS     bool
	}
	groups := map[string]*group{}
	var order []string
	for _, c := range cands {
		g := groups[c.SessionUUID]
		if g == nil {
			g = &group{}
			groups[c.SessionUUID] = g
			order = append(order, c.SessionUUID)
		}
		if c.FTS {
			g.ftsScores = append(g.ftsScores, c.Score)
			g.hasFTS = true
		} else {
			g.likeScores = append(g.likeScores, c.Score)
		}
		g.hitSeqs = append(g.hitSeqs, c.Seq)
	}

	// Rank: FTS sessions first (a full-term match beats substring-only matches),
	// then by combined hit strength within the session's own tier (sum of the top
	// hits, so several relevant turns out-rank one slightly-stronger line). Never
	// sum FTS and LIKE scores together. Deterministic tiebreak on uuid.
	aggOf := make(map[string]float64, len(groups))
	for uuid, g := range groups {
		if g.hasFTS {
			// FTS topKSum, plus a small bounded bonus for short terms the session
			// also covers (in other turns), so a session matching more of the
			// question out-ranks one matching only the FTS term at equal strength —
			// without summing the two incompatible score scales.
			bonus := float64(min(len(g.likeScores), maxHitsPerSession)) * coverageBonus
			aggOf[uuid] = topKSum(g.ftsScores, maxHitsPerSession) + bonus
		} else {
			aggOf[uuid] = topKSum(g.likeScores, maxHitsPerSession)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if gi, gj := groups[order[i]].hasFTS, groups[order[j]].hasFTS; gi != gj {
			return gi
		}
		if aggOf[order[i]] != aggOf[order[j]] {
			return aggOf[order[i]] > aggOf[order[j]]
		}
		return order[i] < order[j]
	})
	if len(order) > opt.MaxSessions {
		order = order[:opt.MaxSessions]
	}

	// Pack whole groups against the token budget: order is already FTS-tier
	// first then descending aggOf, so walking top-down and stopping at the
	// first group that doesn't fit drops the lowest-ranked groups first,
	// always as whole groups. The one exception is the keep-top-hits
	// invariant below, which takes precedence over the budget.
	remaining := opt.MaxTokens
	for i, uuid := range order {
		eg, err := assembleGroup(ctx, database, uuid, aggOf[uuid], groups[uuid].hitSeqs, opt)
		if err != nil {
			return ans, err
		}
		if len(eg.Excerpts) == 0 {
			continue
		}
		if tokens := estimateGroupTokens(eg); tokens <= remaining {
			ans.Groups = append(ans.Groups, eg)
			remaining -= tokens
			continue
		}
		if i == 0 {
			// Invariant (takes precedence over the budget): the top-ranked
			// group's hit excerpts are always returned. When the full group
			// doesn't fit, emit only its hits, truncated toward the floor —
			// so a matching question never yields an empty bundle. This is
			// the sole case where a group is partially emitted; the bundle's
			// effective bound becomes max(opt.MaxTokens, these floor hits).
			if hits := floorTopGroupHits(eg); len(hits.Excerpts) > 0 {
				ans.Groups = append(ans.Groups, hits)
			}
		}
		// This group didn't fit the remaining budget: every group from here
		// down in order is lower-ranked still, so stop rather than skip
		// ahead looking for a smaller one (no bin-packing — a whole
		// lower-ranked group is dropped, not substituted).
		break
	}
	return ans, nil
}

// topKSum sums the k largest scores — a session's combined hit strength, bounded
// so a verbose session can't inflate its rank with many weak hits.
func topKSum(scores []float64, k int) float64 {
	s := append([]float64(nil), scores...)
	sort.Sort(sort.Reverse(sort.Float64Slice(s)))
	sum := 0.0
	for i := 0; i < len(s) && i < k; i++ {
		sum += s[i]
	}
	return sum
}

// assembleGroup windows each hit (up to maxHitsPerSession), merges overlapping
// windows into one ordered excerpt list, marks the hits, and attaches the session
// citation.
func assembleGroup(ctx context.Context, database *db.DB, uuid string, score float64, hitSeqs []int, opt Options) (EvidenceGroup, error) {
	hitSet := make(map[int]bool, len(hitSeqs))
	for _, s := range hitSeqs {
		hitSet[s] = true
	}

	merged := map[int]sessions.Message{}
	used := 0
	for _, hs := range hitSeqs {
		if used >= maxHitsPerSession {
			break
		}
		used++
		win, err := sessions.GetWindow(ctx, database, uuid, hs, opt.Window, opt.Window, false, false)
		if err != nil {
			return EvidenceGroup{}, err
		}
		for _, m := range win {
			merged[m.Seq] = m
		}
	}

	seqs := make([]int, 0, len(merged))
	for s := range merged {
		seqs = append(seqs, s)
	}
	sort.Ints(seqs)

	eg := EvidenceGroup{SessionUUID: uuid, Score: score}
	for _, s := range seqs {
		m := merged[s]
		eg.Excerpts = append(eg.Excerpts, Excerpt{
			Seq:   m.Seq,
			TS:    m.TS,
			Role:  m.Role,
			Text:  truncate(m.Content, opt.MaxExcerptLen),
			IsHit: hitSet[s],
		})
	}

	// Citation metadata: exact-uuid resolve returns title/project/ended_at/source.
	if meta, err := sessions.ResolvePrefix(ctx, database, uuid, "all"); err == nil {
		eg.Title = meta.Title
		eg.Project = meta.ProjectPath
		eg.EndedAt = meta.EndedAt
		eg.Source = meta.Source
	}
	return eg, nil
}

// truncate caps s to n runes (UTF-8 safe), appending an ellipsis when cut.
func truncate(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	i, count := 0, 0
	for i < len(s) && count < n {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
		count++
	}
	return s[:i] + "…"
}

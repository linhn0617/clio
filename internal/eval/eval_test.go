package eval

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/ask"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/search"
)

// newCorpusDB opens a fresh temp DB and loads the fixture corpus into it,
// exactly as ask_test.go / search_test.go build their own temp DBs
// (db.Open(t.TempDir()...) then INSERTs), so recency-dependent ranking
// behaves identically to production.
func newCorpusDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "eval.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := loadCorpus(d); err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	return d
}

// evalIsCJK mirrors ask's unexported isCJK (internal/ask/terms.go) — a local
// copy used only for the suite's snippet-visibility heuristic below, not for
// enforcement (matching the same "mirror, don't import" pattern the design
// uses for MCP/CLI budget constants).
func evalIsCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

func hasCJKRune(s string) bool {
	for _, r := range s {
		if evalIsCJK(r) {
			return true
		}
	}
	return false
}

// queryWords approximates a query's content terms for the suite's snippet-
// visibility check. It intentionally does NOT reproduce search's or ask's
// internal tokenizer (CJK trigram/bigram expansion, stopword removal,
// long/short partitioning) — those are unexported, and the suite only needs
// "does the visible snippet/excerpt retain something the user typed", not
// exact parity with retrieval's own term extraction.
//
//   - ASCII/other content: lowercased, whitespace-split, punctuation trimmed
//     from each field's ends.
//   - CJK content: every contiguous 2-rune substring ("bigram") of each CJK
//     run in the query, checked by overlap rather than whole-string
//     containment. A natural-language CJK question (e.g. "資料庫遷移的狀況
//     如何？") is not itself a substring of the answer content — only a
//     fragment of it is (e.g. "資料庫遷移") — and ask's own retrieval
//     extracts bigram/trigram grams from segmented content, not whole-
//     question substrings (internal/ask/terms.go's expand/segmentCJK).
//     Bigram overlap is the robust proxy the task calls for: it needs no
//     reproduction of ask's private CJK stopword segmentation, and (unlike
//     asserting a single exact gram) is not fragile against an FTS
//     snippet() truncating mid-gram at a window boundary.
func queryWords(q string) []string {
	q = strings.ReplaceAll(q, `"`, "")
	var out []string

	for _, f := range strings.Fields(strings.ToLower(q)) {
		f = strings.Trim(f, ".,?!;:()[]{}，。！？；：、「」『』（）")
		if f != "" && !hasCJKRune(f) {
			out = append(out, f)
		}
	}

	runes := []rune(q)
	for i := 0; i < len(runes); i++ {
		if !evalIsCJK(runes[i]) {
			continue
		}
		j := i
		for j < len(runes) && evalIsCJK(runes[j]) {
			j++
		}
		run := runes[i:j]
		for k := 0; k+2 <= len(run); k++ {
			out = append(out, string(run[k:k+2]))
		}
		i = j - 1
	}
	return out
}

// containsAnyWord reports whether text (case-folded) contains at least one
// of words as a substring.
func containsAnyWord(text string, words []string) bool {
	lc := strings.ToLower(text)
	for _, w := range words {
		if w != "" && strings.Contains(lc, w) {
			return true
		}
	}
	return false
}

// TestSearchRegression asserts search.Search's binary expectations in
// testdata/search_queries.json: each expected (session, seq) hit appears
// within its declared top_k, and the returned snippet contains at least one
// extracted query term (snippet visibility). Per
// specs/retrieval-eval/spec.md's "Assertion-based regression queries, Search
// and Ask asserted separately" requirement, this is Search-only — it never
// exercises ask.Ask.
func TestSearchRegression(t *testing.T) {
	d := newCorpusDB(t)
	qf, err := loadQueryFile("search_queries.json")
	if err != nil {
		t.Fatalf("load search_queries.json: %v", err)
	}
	if len(qf.Queries) == 0 {
		t.Fatal("search_queries.json has no queries")
	}

	var durations []time.Duration
	for _, qc := range qf.Queries {
		t.Run(qc.ID, func(t *testing.T) {
			start := time.Now()
			results, err := search.Search(context.Background(), d, search.Options{Query: qc.Query, Limit: 20})
			durations = append(durations, time.Since(start))
			if err != nil {
				t.Fatalf("query %s (%q): Search error: %v", qc.ID, qc.Query, err)
			}

			words := queryWords(qc.Query)
			for _, exp := range qc.Expect {
				idx := -1
				for i, r := range results {
					if r.SessionUUID == exp.Session && r.Seq == exp.Seq {
						idx = i
						break
					}
				}
				if idx < 0 || idx >= exp.TopK {
					t.Fatalf("query %s (%q): expected session=%s seq=%d within top_k=%d; got top-k %s",
						qc.ID, qc.Query, exp.Session, exp.Seq, exp.TopK, describeSearchTopK(results, exp.TopK))
				}
				// Snippet visibility (scenario "An invisible match fails the suite").
				if !containsAnyWord(results[idx].Snippet, words) {
					t.Fatalf("query %s (%q): hit session=%s seq=%d has no visible query term in snippet %q",
						qc.ID, qc.Query, exp.Session, exp.Seq, results[idx].Snippet)
				}
			}
		})
	}
	logLatency(t, "search", durations)
}

// TestAskRegression asserts ask.Ask's binary expectations in
// testdata/ask_queries.json: each expected session appears among the top_k
// groups, its declared hit_seq (when present) is marked IsHit and its
// excerpt text is visible (contains a query term), and the bundle's
// estimated tokens stay within its effective budget (max(configured budget,
// top group's floor-truncated hit tokens) per specs/ask/spec.md and
// specs/retrieval-eval/spec.md's "over-budget ask bundle fails" /
// "invariant-driven overage is not a failure" scenarios). This is Ask-only —
// it never exercises search.Search directly.
func TestAskRegression(t *testing.T) {
	d := newCorpusDB(t)
	qf, err := loadQueryFile("ask_queries.json")
	if err != nil {
		t.Fatalf("load ask_queries.json: %v", err)
	}
	if len(qf.Queries) == 0 {
		t.Fatal("ask_queries.json has no queries")
	}

	// Suite budget is ask's package default (2000): comfortably above the
	// floor for this corpus's short fixture messages, per design.md section 4
	// ("Ask queries in the suite use budgets comfortably above the floor").
	// Options{} leaves MaxTokens at 0, which ask.Ask defaults internally.
	var durations []time.Duration
	for _, qc := range qf.Queries {
		t.Run(qc.ID, func(t *testing.T) {
			start := time.Now()
			ans, err := ask.Ask(context.Background(), d, ask.Options{Question: qc.Query})
			durations = append(durations, time.Since(start))
			if err != nil {
				t.Fatalf("query %s (%q): Ask error: %v", qc.ID, qc.Query, err)
			}

			// Ask bundle budget: the effective bound is max(configured budget,
			// the top group's minimum-length hit excerpts) — mirroring the
			// keep-top-hits invariant so correct invariant behavior is never
			// reported as a failure (spec's "Invariant-driven overage is not a
			// failure" scenario). At the suite's generous default budget the
			// invariant floor never exceeds it for this corpus, so the
			// effective bound is simply the configured budget in practice.
			budget := effectiveAskBudget(ans, evalAskBudget)
			if got := bundleTokens(ans); got > budget {
				t.Fatalf("query %s (%q): bundle estimated %d tokens, exceeds effective budget %d",
					qc.ID, qc.Query, got, budget)
			}

			words := queryWords(qc.Query)
			for _, exp := range qc.Expect {
				gi := -1
				for i, g := range ans.Groups {
					if g.SessionUUID == exp.Session {
						gi = i
						break
					}
				}
				if gi < 0 || gi >= exp.TopK {
					t.Fatalf("query %s (%q): expected session=%s within top_k=%d groups; got top-k %s",
						qc.ID, qc.Query, exp.Session, exp.TopK, describeAskTopK(ans, exp.TopK))
				}
				if exp.HitSeq == nil {
					continue
				}
				g := ans.Groups[gi]
				var hit *ask.Excerpt
				for i := range g.Excerpts {
					if g.Excerpts[i].Seq == *exp.HitSeq {
						hit = &g.Excerpts[i]
						break
					}
				}
				if hit == nil {
					t.Fatalf("query %s (%q): expected hit_seq=%d not present in session=%s excerpts: %+v",
						qc.ID, qc.Query, *exp.HitSeq, exp.Session, g.Excerpts)
				}
				if !hit.IsHit {
					t.Fatalf("query %s (%q): expected hit_seq=%d in session=%s to be marked is_hit; excerpt=%+v",
						qc.ID, qc.Query, *exp.HitSeq, exp.Session, *hit)
				}
				// Snippet visibility on the hit excerpt.
				if !containsAnyWord(hit.Text, words) {
					t.Fatalf("query %s (%q): hit_seq=%d in session=%s has no visible query term in excerpt %q",
						qc.ID, qc.Query, *exp.HitSeq, exp.Session, hit.Text)
				}
			}
		})
	}
	logLatency(t, "ask", durations)
}

func describeSearchTopK(results []search.Result, n int) string {
	if n > len(results) {
		n = len(results)
	}
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(results[i].SessionUUID)
		sb.WriteString("#")
		sb.WriteString(strconv.Itoa(results[i].Seq))
	}
	sb.WriteString("]")
	return sb.String()
}

func describeAskTopK(ans ask.Answer, n int) string {
	if n > len(ans.Groups) {
		n = len(ans.Groups)
	}
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(ans.Groups[i].SessionUUID)
	}
	sb.WriteString("]")
	return sb.String()
}

// logLatency reports mean/worst per-query latency for visibility, per
// specs/retrieval-eval/spec.md: "Per-query-set latency SHALL be reported for
// visibility but SHALL NOT be a pass/fail assertion (it is
// machine-dependent)."
func logLatency(t *testing.T, set string, durations []time.Duration) {
	t.Helper()
	if len(durations) == 0 {
		return
	}
	var sum, worst time.Duration
	for _, d := range durations {
		sum += d
		if d > worst {
			worst = d
		}
	}
	mean := sum / time.Duration(len(durations))
	t.Logf("%s set: n=%d mean=%s worst=%s", set, len(durations), mean, worst)
}

// evalAskBudget mirrors ask's unexported defaultMaxTokens (internal/ask/tokens.go)
// — not imported, the same way internal/mcp and internal/cli already mirror
// ask's MaxTokens bounds instead of importing them (see design.md section 3).
const evalAskBudget = 2000

// evalMinExcerptRunes mirrors ask's unexported minExcerptRunes
// (internal/ask/tokens.go), used only to reconstruct the keep-top-hits
// invariant's floor bound for the budget assertion below.
const evalMinExcerptRunes = 80

// bundleTokens is a bundle's total estimated tokens: the same measurement
// ask.Ask itself uses to enforce the budget (EstimateTokens over each
// excerpt's Text), so enforcement and assertion can never diverge.
func bundleTokens(ans ask.Answer) int {
	total := 0
	for _, g := range ans.Groups {
		for _, e := range g.Excerpts {
			total += ask.EstimateTokens(e.Text)
		}
	}
	return total
}

// effectiveAskBudget mirrors the keep-top-hits invariant in
// specs/ask/spec.md: the bundle's effective bound is the larger of the
// configured budget and the top-ranked group's hit excerpts truncated to the
// floor. For this suite's generous default budget the floor never exceeds
// it, so this returns budget in practice — but the general formula is
// implemented so an over-budget bundle is never misreported as invariant
// overage (scenario "Invariant-driven overage is not a failure").
func effectiveAskBudget(ans ask.Answer, budget int) int {
	if len(ans.Groups) == 0 {
		return budget
	}
	floor := 0
	for _, e := range ans.Groups[0].Excerpts {
		if !e.IsHit {
			continue
		}
		text := e.Text
		if r := []rune(text); len(r) > evalMinExcerptRunes {
			text = string(r[:evalMinExcerptRunes]) + "…"
		}
		floor += ask.EstimateTokens(text)
	}
	if floor > budget {
		return floor
	}
	return budget
}

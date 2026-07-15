package ask

// Token budget constants for the ask evidence bundle (see design.md in
// openspec/changes/2026-07-14-retrieval-eval-and-ask-budget for the full
// rationale). defaultMaxTokens/minMaxTokens/maxMaxTokens are mirrored — not
// imported — by the MCP and CLI surfaces, the same way defaultAskSessions /
// maxAskSessions in internal/mcp already mirror ask's session-count defaults.
const (
	defaultMaxTokens = 2000 // global budget for the bundle's excerpt text
	minMaxTokens     = 200  // safe floor for a caller-supplied budget
	maxMaxTokens     = 8000 // safe ceiling for a caller-supplied budget
	minExcerptRunes  = 80   // floor for the top group's hit excerpts when the budget is very small
)

// EstimateTokens approximates the token cost of s for the ask bundle budget: a
// dependency-free, deterministic heuristic — CJK runes (dense scripts) count as
// one token each, other runes as roughly four characters per token (typical of
// BPE tokenization of Latin text). It reuses the existing isCJK classifier. The
// same function enforces the budget in Ask and (in the retrieval regression
// suite) asserts it, so enforcement and measurement can never diverge.
func EstimateTokens(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if isCJK(r) {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other+3)/4 // ceil(other/4)
}

// estimateGroupTokens is a group's contribution to the bundle budget: the sum
// of EstimateTokens over its excerpt text. Titles, timestamps, and role labels
// are not counted (see design's "scope of measurement") — only the payload an
// LLM actually reads.
func estimateGroupTokens(eg EvidenceGroup) int {
	total := 0
	for _, e := range eg.Excerpts {
		total += EstimateTokens(e.Text)
	}
	return total
}

// floorTopGroupHits keeps only eg's hit excerpts, each truncated to
// minExcerptRunes. It implements the keep-top-hits invariant's floor form: used
// only when the top-ranked group's full assembly does not fit the remaining
// budget, so the bundle still returns something rather than nothing.
func floorTopGroupHits(eg EvidenceGroup) EvidenceGroup {
	hits := EvidenceGroup{
		SessionUUID: eg.SessionUUID,
		Title:       eg.Title,
		Project:     eg.Project,
		EndedAt:     eg.EndedAt,
		Score:       eg.Score,
		Source:      eg.Source,
	}
	for _, e := range eg.Excerpts {
		if !e.IsHit {
			continue
		}
		e.Text = truncate(e.Text, minExcerptRunes)
		hits.Excerpts = append(hits.Excerpts, e)
	}
	return hits
}

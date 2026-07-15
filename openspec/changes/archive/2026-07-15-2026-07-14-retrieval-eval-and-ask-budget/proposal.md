## Why

Two gaps in the retrieval layer, both raised by a codex (GPT) review:

1. **Retrieval quality has unit tests but no end-to-end regression protection.** The
   ranking stack's hand-tuned constants are each covered in isolation —
   `roleWeights`/recency by `TestAdjustedScoreRoleWeighting` and
   `TestAdjustedScoreRecency` (`internal/search/rank_test.go`), the FTS-vs-LIKE tiering
   and `shortAlsoBoost` by `TestRetrieveFTSTierRanksBeforeLikeOnly` and
   `TestRetrieveBothTierHitOutranksLongOnly` (`internal/search/retrieve_test.go`), and
   `topKSum`/`coverageBonus` by `TestTopKSumRewardsMultipleHits` and
   `TestAskRanksFullerCoverageHigher` (`internal/ask/ask_test.go`). What no test covers
   is their **interaction end to end**: a realistic bilingual corpus where term
   extraction, tiering, role/recency weighting, session grouping, and windowing all
   compose — the level at which a future tweak to one constant can silently break a
   query class (e.g. CJK bigrams, quoted phrases, code fragments) while every unit
   test still passes. That end-to-end interaction coverage is the net new value this
   change adds.

2. **`ask` bundles have no global size budget.** `ask` caps a single excerpt at
   `defaultMaxExcerptLen = 600` runes (`internal/ask/ask.go`), but nothing bounds the
   *whole* bundle: up to `maxAskSessions = 10` groups (`internal/mcp/tools.go`), each
   merging up to `maxHitsPerSession = 3` windows of `defaultWindow = 2` turns each
   side. A single `ask` response can reach several thousand tokens. For the tool's
   core purpose — feeding a cited bundle into an LLM's context for synthesis — that
   is an uncontrolled cost.

The two are coupled: the budget (2) is a behavior the regression suite (1) asserts, so
the bundle size can never silently regress either.

## What Changes

- **Added** a global token budget for the `ask` evidence bundle. A new
  `ask.EstimateTokens` (deterministic, dependency-free: CJK runes counted 1:1, other
  runes ~4:1, reusing the existing `isCJK`) measures the bundle's excerpt text, and
  `Ask` packs whole groups against a total budget (default 2000 tokens): ranked groups
  are added top-down and the lowest-ranked whole groups are dropped once the budget is
  spent. One invariant takes precedence over the budget: the top-ranked group's hit
  excerpts are always emitted, truncated toward a minimum excerpt length when needed —
  so the bundle's effective bound is the larger of the budget and that minimum-length
  floor. The existing per-excerpt 600-rune cap is retained as an inner ceiling.
  Exposed as MCP `ask` param `max_tokens` and CLI `clio ask --max-tokens`.

- **Added** an assertion-style retrieval regression suite in a new `internal/eval`
  package, running under plain `go test ./...` (no build tag: the corpus loads in
  milliseconds, and a tag would only split the suite into a false-green default run;
  the existing CI `go test -race ./...` covers it with no workflow change). It loads a
  **small, hand-written, sanitized bilingual fixture corpus** (~10–12 sessions
  committed under `internal/eval/testdata/`, not derived from any private history)
  into a temp DB and asserts, per query, binary expectations with self-explanatory
  failure messages: the expected session/message appears in the top-k of
  `search.Search` / `ask.Ask` (Search and Ask asserted separately), the returned
  snippet contains a query term (snippet visibility), and each `ask` bundle's
  estimated tokens stay within its effective budget. No graded relevance, no
  Recall/MRR/nDCG, no recorded baseline: over a corpus this small those aggregates are
  step functions, so a tolerance gate would be either flaky or vacuous — absolute
  quality scoring is an explicit non-goal for now.

No ranking constant, retrieval algorithm, or existing spec behavior is changed by this
proposal except the addition of the `ask` budget; the regression suite only asserts.

## Capabilities

### Added Capabilities

- `retrieval-eval`: a deterministic retrieval regression suite under plain `go test`
  over a small hand-written bilingual fixture corpus — per-query binary assertions
  (expected hit in top-k, snippet visibility, `ask` bundle within its effective
  budget) for Search and Ask separately, protecting the end-to-end interaction of the
  ranking stack.

### Modified Capabilities

- `ask`: the evidence bundle is packed to a global token budget (default 2000) by
  dropping lowest-ranked whole groups, with a keep-top-hits invariant that takes
  precedence over the budget; a deterministic token estimator measures bundle size.
- `mcp-server`: the `ask` tool gains a `max_tokens` parameter.
- `cli-surface`: `clio ask` gains a `--max-tokens` flag.

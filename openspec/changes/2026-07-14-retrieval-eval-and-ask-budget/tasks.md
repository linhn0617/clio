Implement (b) the ask token budget first, then (a) the regression suite — the suite
asserts (b)'s bundle-size behavior, so (b) must exist to be asserted.

## 1. Global token budget for the ask bundle (`internal/ask`)

- [x] 1.1 `EstimateTokens(s string) int` (exported, in `ask.go` or a new `tokens.go`):
  count CJK runes 1:1 via the existing `isCJK`, other runes as `ceil(n/4)`; sum. Pure,
  deterministic, no deps (TDD: ASCII-only, CJK-only, mixed, empty, whitespace).
- [x] 1.2 Constants `defaultMaxTokens = 2000`, `minMaxTokens = 200`, `maxMaxTokens = 8000`,
  `minExcerptRunes = 80`; add `MaxTokens int` to `ask.Options`; default it in `Ask`
  (`if opt.MaxTokens <= 0 { opt.MaxTokens = defaultMaxTokens }`) next to the existing
  `MaxSessions`/`Window`/`MaxExcerptLen` defaults.
- [x] 1.3 Whole-group budget packing in `Ask`: after `order` is ranked and capped to
  `MaxSessions`, walk it top-down; emit a group only if its total
  `EstimateTokens(excerpt.Text)` fits the remaining budget, else stop (drops
  lowest-tier / lowest-`aggOf` whole groups first, since `order` is FTS-first then
  descending). **Invariant, taking precedence over the budget:** the top group's
  `IsHit` excerpts are always emitted — if the full top group does not fit, emit only
  its hit excerpts, each truncated to fit but never below `minExcerptRunes`; the
  effective bound is `max(MaxTokens, top-group hits at the floor)`. No finer trimming
  layers (no non-hit ordering, no re-truncation of fitting groups). The per-excerpt
  `truncate`-to-`MaxExcerptLen` inner cap is unchanged (TDD).
- [x] 1.4 MCP surface (`internal/mcp/server.go` + `tools.go`): add
  `mcp.WithNumber("max_tokens", Description(...), DefaultNumber(defaultMaxTokens),
  Min(minMaxTokens), Max(maxMaxTokens))` to the `ask` tool; in `handleAsk` set
  `MaxTokens: clamp(req.GetInt("max_tokens", defaultMaxTokens), minMaxTokens, maxMaxTokens)`.
  (Define MCP-local `defaultMaxTokens`/bounds mirroring the `ask` package, matching how
  `defaultAskSessions`/`maxAskSessions` already mirror there.)
- [x] 1.5 CLI surface (`internal/cli/ask.go`): add `--max-tokens` int flag (default 0 →
  package default) wired to `ask.Options.MaxTokens`.
- [x] 1.6 Tests: table-driven `EstimateTokens`; `Ask` budget tests over a temp db
  (`db.Open(t.TempDir()…)`, `addSession`/`addMsg` as `ask_test.go` does) asserting:
  a large-history question stays within the budget; whole lower-ranked groups are
  dropped, never partially emitted; the top group's hits survive a tiny budget at the
  floor (bundle may exceed the budget then — assert the effective bound, not the raw
  budget); a generous budget reproduces the pre-budget bundle. Assert the MCP/CLI
  knobs thread through.

## 2. Retrieval regression suite (new `internal/eval`)

- [x] 2.1 Hand-written fixture corpus `internal/eval/testdata/corpus/sessions.jsonl`
  (~10–12 sessions): EN-only, CJK-only (Traditional Chinese), mixed EN/CJK,
  code-fragment content (`foo("bar`, `c++`), quoted-phrase targets, and at least one
  pair exercising the FTS-vs-LIKE tier split. Fully synthetic and sanitized — NOT
  scraped from real history. Each message carries a fixed `offset_days` so the loader
  sets deterministic relative `ts`.
- [x] 2.2 `corpus.go`: load the corpus into a temp `db.DB` (`db.Open` + INSERTs into
  `sessions`/`messages` exactly as `ask_test.go`'s helpers do), setting each
  `ts = now - offset_days*86400`. Returns the db + the expectation sets.
- [x] 2.3 Expectation sets `search_queries.json` and `ask_queries.json` (binary):
  Search keys on `(session, seq)` in top-k; Ask keys on `session` among top-k groups,
  optionally with a `hit_seq` that must be marked `is_hit`. Cover EN / CJK / mixed /
  code / quoted-phrase queries; Search and Ask asserted as **separate** sets in
  separate test functions.
- [x] 2.4 `eval_test.go` (no build tag, runs in plain `go test ./...`): load corpus, run
  `search.Search` over the search set and `ask.Ask` over the ask set; per query assert
  (i) every expected item within its `top_k`, (ii) snippet visibility — the returned
  snippet / hit excerpt contains at least one extracted query term, (iii) each ask
  bundle's `EstimateTokens` within its effective budget (`max(budget, top-group floor
  hits)`; suite budgets are set comfortably above the floor). Failure messages include
  the query id, the expectation, and the actual top-k. Latency per set reported via
  `t.Logf`, not asserted.

## 3. Verify

- [x] 3.1 `go build ./...`, `go vet ./...`, `go test -race ./...` all green
  (the new `internal/eval` suite runs in the default test set); `gofmt -l .` clean.
- [x] 3.2 `openspec validate 2026-07-14-retrieval-eval-and-ask-budget --strict` passes.
- [ ] 3.3 Manual check: `clio ask "<big-history question>" --max-tokens 500 --json` and an
  MCP `ask` call with `max_tokens: 500` both return a bundle whose excerpt text estimates
  ≤ ~500 tokens, still leading with the most relevant session's hits.
  (Not run this pass — out of scope for the eval-suite/docs task; section 1's `ask`
  budget implementation was already in place and unit-tested before this task started.)
- [ ] 3.4 Codex review of the real diff to a clean gate (re-review after each fix), then
  Claude `/review`.
  (Skipped this pass: codex quota exhausted per task instructions. Still needs a real
  codex round + Claude `/review` before this change is considered done.)
- [~] 3.5 Docs: CHANGELOG entry; README ×2 note the `ask` `--max-tokens` / `max_tokens`
  budget and the `internal/eval` retrieval regression suite.
  (README.md, README_zh-TW.md, and docs/USAGE.md done this pass. CHANGELOG entry
  deliberately NOT added — task instructions reserve CHANGELOG/openspec-archive for
  release time.)

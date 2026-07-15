## Context

Retrieval spans two packages:

- `internal/search`: `Search` (`search.go`) for AND queries and `Retrieve`
  (`retrieve.go`) for the OR any-term pool that `ask` consumes. Ranking is
  `adjustedScore` = `-bm25 * roleWeights[role] + recencyBonus(ts)` (`rank.go`), with
  `overscan = 12`, the FTS-vs-LIKE tiering, and `shortAlsoBoost = 0.5`. Snippets come
  from FTS `snippet(messages_fts,0,'[',']','…',64)` (hybrid path) or Go-side
  `windowSnippet` / `firstTermRunePos` (LIKE path).
- `internal/ask`: `Ask` (`ask.go`) retrieves a pool via `search.Retrieve`, groups by
  session, ranks with `topKSum` + `coverageBonus`, windows each hit via
  `sessions.GetWindow`, and truncates each excerpt with `truncate` to
  `defaultMaxExcerptLen = 600`. Term extraction (`terms.go`) expands CJK into
  trigrams/bigrams; `isCJK` classifies CJK runes.

Each ranking constant already has an isolated unit test (see proposal); the missing
coverage is end-to-end interaction, which the regression suite below provides.

Two facts constrain the design:

- **`recencyBonus` is time-dependent.** `adjustedScore` calls
  `recencyBonus(ts)` → `time.Since(time.Unix(ts,0))`, so absolute scores drift with
  wall-clock time. The fixture loader assigns each message a fixed `offset_days`
  relative to load time, so relative ages — and therefore the recency-driven ordering —
  are constant across runs.
- **`ask` exposes only some knobs.** MCP `ask` exposes `question/since/project/limit/source`
  (`server.go`); it does **not** expose `Window` or `MaxExcerptLen`. CLI exposes
  `--window` but not `MaxExcerptLen`. The new budget must be threaded through both
  surfaces explicitly.

## Decision

### 1. Token estimator (`internal/ask`, exported `EstimateTokens`)

A dependency-free, deterministic approximation — no tokenizer library, no network
(consistent with `ask`'s "no network" contract):

```
EstimateTokens(s): cjk = count of runes r where isCJK(r); other = runeCount(s) - cjk
                   return cjk + ceil(other / 4)
```

CJK is ~1 token/char, Latin text ~4 chars/token. It reuses the existing `isCJK`.
It is explicitly an approximation of BPE tokenization, chosen so the *same* function
enforces the budget in `Ask` and asserts it in the regression suite — they can never
diverge.

**Scope of the measurement:** the budget counts excerpt **`Text`** only — the payload
an LLM reads. It does not count titles, timestamps, role labels, or the JSON
scaffolding the MCP handler adds around the bundle (`handleAsk` in
`internal/mcp/tools.go`). The budget is therefore an approximate bound on the dominant
payload, not an exact bound on the wire-level response size.

### 2. Budget packing in `Ask` (`internal/ask/ask.go`)

Add `MaxTokens int` to `ask.Options`, defaulting to `defaultMaxTokens = 2000` when ≤0.
After sessions are ranked into `order` (FTS-tier first, then by `aggOf`) and capped to
`MaxSessions`, pack **whole groups** against a running budget:

- Walk `order` top-down. For each group, `assembleGroup` builds its excerpts as today
  (per-excerpt `truncate` to `MaxExcerptLen` still applies as the inner cap). If the
  group's total `EstimateTokens` fits the remaining budget, emit it; otherwise stop —
  because `order` is FTS-tier-first then descending `aggOf`, this drops the
  lowest-ranked (LIKE-tier, then lowest-agg) whole groups first. No finer-grained
  trimming layers: no per-excerpt re-truncation of fitting groups, no ordering of
  non-hit turns for partial drops.
- **Invariant (takes precedence over the budget):** the top-ranked group's hit
  excerpts (`IsHit == true`) are always emitted. If the full top group does not fit
  the budget, emit only its hit excerpts, each truncated to fit the budget but never
  below a floor `minExcerptRunes` (e.g. 80). The bundle's effective bound is therefore
  `max(MaxTokens, EstimateTokens of the top group's hit excerpts at the floor)` — a
  tiny budget (e.g. the minimum 200 with three CJK hits at the floor) yields a bundle
  slightly over budget rather than an empty one, by design.

Determinism: the walk consumes the already-deterministic `order` and seq-sorted
excerpts; ties already broken by uuid/seq. No new nondeterminism.

### 3. Surfaces (`internal/mcp`, `internal/cli`)

- MCP `ask` (`server.go`): add `mcp.WithNumber("max_tokens", …, DefaultNumber(defaultMaxTokens), Min(minMaxTokens), Max(maxMaxTokens))`; `handleAsk` (`tools.go`) sets `MaxTokens: clamp(req.GetInt("max_tokens", defaultMaxTokens), …)`.
- CLI `clio ask` (`ask.go`): add `--max-tokens` int flag → `ask.Options.MaxTokens`
  (near-zero cost, keeps the CLI and MCP surfaces symmetric).
- Suggested bounds: `defaultMaxTokens = 2000`, `minMaxTokens = 200`, `maxMaxTokens = 8000`.
  `MaxExcerptLen` stays internal (unchanged; not newly exposed).

### 4. Regression suite (`internal/eval`, plain `go test`)

Layout:

```
internal/eval/
  corpus.go          # load testdata corpus into a temp db (db.Open + INSERTs, as ask_test.go does)
  eval_test.go       # assertion-style regression tests over search.Search and ask.Ask
  testdata/
    corpus/sessions.jsonl   # ~10–12 hand-written sanitized bilingual sessions
    search_queries.json     # per-query binary expectations for search.Search
    ask_queries.json        # per-query binary expectations for ask.Ask
```

- **No build tag.** The corpus loads in milliseconds; the suite runs in the default
  `go test ./...` and is therefore already covered by CI's `go test -race ./...`. A
  tag would only split the suite and let the default run go green while retrieval
  regresses.
- **Corpus** (~10–12 hand-written sessions) covers the query classes the ranking stack
  distinguishes: EN-only, CJK-only (Traditional Chinese), mixed EN/CJK, code fragments
  (e.g. `foo("bar`, `c++`), quoted phrases, and pairs designed to exercise the
  FTS-vs-LIKE tier split (a long-term match vs a short/bigram-only match). Fully
  synthetic and sanitized — never scraped from real history (which would overfit and
  be unshareable). Each message carries a fixed `offset_days`; the loader sets
  `ts = now − offset_days*86400`, keeping the recency ordering constant across runs.
- **Expectation schema** (binary, self-explanatory):
  ```json
  {"queries":[{"id":"search-en-auth-01","query":"refresh token auth","lang":"en",
    "expect":[{"session":"s-auth","seq":12,"top_k":5}]}]}
  ```
  Search expectations key on `(session, seq)` appearing in the top-k results; Ask
  expectations key on `session` appearing among the top-k groups (optionally with a
  `hit_seq` whose excerpt must be marked `is_hit`). Search and Ask are separate files
  and separate test functions.
- **Assertions per query** (each failure message names the query id, what was expected,
  and the actual top-k so it is diagnosable without rerunning):
  1. every expected item appears within its `top_k`;
  2. **snippet visibility** — the returned snippet (`Result.Snippet`) / hit excerpt
     (`Excerpt.Text`) contains at least one extracted query term;
  3. **ask bundle budget** — each bundle's `EstimateTokens` total is within its
     effective budget, i.e. `max(configured budget, top group's floor-truncated hits)`,
     mirroring the invariant in §2 so correct invariant behavior is never reported as
     a failure. Ask queries in the suite use budgets comfortably above the floor, so
     in practice the assertion is against the configured budget.
- **Latency** is reported via `t.Logf` per query set (mean/worst) for visibility but is
  not asserted — it is machine-dependent.

## Trade-offs / risks

- **`EstimateTokens` is approximate.** Not a real BPE tokenizer, and it measures only
  excerpt text (§1), so the budget is a proxy for response cost. Accepted: exactness
  would require a tokenizer dependency and per-model variance; one shared estimator for
  enforce+assert keeps the system consistent, which matters more than token fidelity.
- **The invariant can exceed the budget.** A caller setting a tiny budget can receive
  slightly more than asked (top group's floor-truncated hits). Accepted and specified:
  an empty bundle for a matching question is a worse contract than one minimal group.
- **Binary assertions are coarse.** They catch "an expected hit fell out of top-k /
  lost its snippet / blew the budget", not gradual ordering drift among still-passing
  results. Accepted for v1: on a ~10-session corpus, finer aggregates (Recall/MRR/nDCG)
  are step functions and gate poorly; growing the corpus and adding absolute quality
  scoring is future work.
- **Corpus realism.** Hand-written fixtures may not mirror real query/relevance
  distributions. Accepted for shareability and determinism; the corpus can grow over
  time.

## Non-goals

- No vector / embedding / semantic retrieval.
- No LLM-as-judge evaluation (kept deterministic and offline).
- No aggregate quality metrics (Recall@k, MRR, nDCG), no recorded baseline/tolerance
  gate, no `-update` flow — deferred until a corpus large enough to make them
  meaningful exists.
- No tuning of existing ranking constants in this change — the suite enables that as
  future work; here it only asserts (plus the one new `ask` budget behavior).
- No use of real/private conversation history as fixtures.

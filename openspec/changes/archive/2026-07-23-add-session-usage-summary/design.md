# Design: add-session-usage-summary

Revised 2026-07-21 after second-round codex feasibility review (GO-WITH-CHANGES ×9, marked
`[RC-n]`), and again 2026-07-22 after third-round review (GO-WITH-CHANGES ×6, marked `[RC3-n]`).

## Context

All three source JSONLs carry token usage that clio's parsers currently drop or ignore:
Claude Code puts `message.usage` on assistant messages (with outer `uuid` and `message.model`),
Codex emits `token_count` outer events that the adapter skips entirely (codex.go event-type
switch), and Gemini records a `tokens` object per message. Codex rollouts additionally persist
`rate_limits` payloads (used_percent / window_minutes / resets_at / plan_type — verified against
a real 2026-07-20 rollout). The existing architecture is one source file = one session
(`sessions.source_file` is scalar; a second full commit for the same UUID deletes and rewrites),
incremental ingest reads only new tails for Claude/Codex, and Gemini does whole-file replay.
Per-file diagnostic counters live in `ingest_state`.

**The `messages` table is a filtered subset of the source file, not complete state** [RC3-1]:
the parser drops `mcp__clio__*` tool-use blocks entirely (parser.go:121-124), and an assistant
event whose only block is such a call produces zero message rows (TestParseSelfPollutionSkipped
asserts this) — yet that event still carries `message.usage`. Stored `raw_json` is also
redaction-processed. Any usage design that treats the DB as complete Claude state is therefore
wrong; this design reads the original session file instead.

Scope was set by adversarial cross-model review: session-level aggregates only, no per-message
usage rows, no persisted dollar cost, quota snapshots with explicit staleness, quota data never
over MCP.

## Goals / Non-Goals

**Goals:**
- One usage aggregate row per (session, model) — deterministic, idempotent, replace-semantics —
  produced during normal ingest for all three sources.
- Codex quota snapshots stored with `observed_at` and rendered with staleness, never as live
  data, CLI-only.
- Usage visible in CLI (`clio usage`, `list`), TUI, and MCP `activity_summary` (token aggregates
  only), with jump-through to session content at session granularity.
- Explicit, numeric performance gates with a reproducible measurement protocol, including the
  new long-session tail-ingest gate (see Verification Gates). [RC3-3][RC3-5]

**Non-Goals:**
- Per-message usage rows or usage-ranked message indexes (this includes "persist every
  usage-bearing raw event" — rejected as per-message rows by another name [RC3-1]).
- Any dollar/cost figure, persisted or displayed. MVP shows token counts only; no query-time
  `estimated_cost` either (a pricing source is a new dependency — separate change if ever).
- Live quota for Claude Code (statusline integration) or Gemini (quota API) — separate changes.
  No "unified three-CLI quota" claim anywhere until those exist.
- Quota over MCP, in any form, flag or not. [RC-7]
- Cross-source combined token totals (different tokenizers are not comparable) and subagent
  usage roll-up into parents (follow-up). [RC-8]
- A config-file subsystem (clio has none today; nothing in this change introduces one). [RC-7]
- Per-model attribution for Codex (MVP rows are always `(mixed)`; see D2). [RC3-4]
- Retention policies and per-project MCP filtering (follow-up).

## Decisions

**D1 — Storage: companion table keyed (session_uuid, model), source-tagged. [RC-4][RC-5][RC-8]**
New migration `0011_session_usage.sql`:
- `session_usage(session_uuid TEXT NOT NULL, source TEXT NOT NULL, model TEXT NOT NULL,
  input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens,
  tool_tokens, total_tokens, categories_json TEXT, PRIMARY KEY(session_uuid, model))`.
  - `total_tokens` semantics (ranking is defined by this column, so it must be reproducible)
    [RC4-5]: when the source provides a native total (Codex `total_token_usage.total_tokens`,
    Gemini `tokens.total`), store the native value; otherwise (Claude) store the fixed derived
    sum input + output + cache_read + cache_creation. Categories that only exist in
    `categories_json` are never added to `total_tokens`. Cross-source comparability is not a
    goal (surfaces are per-source sections), only per-source reproducibility is.
  - `model` is NOT NULL with sentinels `(unknown)` / `(mixed)` — SQLite composite PKs admit
    multiple NULLs, so a sentinel is required. Per-model rows exist only when the source
    genuinely attributes usage to a model (Claude/Gemini); Codex rows are always `(mixed)` in
    this change (see D2). A future per-model-attribution change MUST delete the sentinel row
    before inserting named-model rows, so sentinel and named rows are never summed together.
    [RC3-4]
  - `categories_json` holds **per-category summed** canonical JSON of source-specific categories
    that don't map to the canonical columns (an aggregate like every other column, not a
    verbatim event copy).
  - Lifecycle: session deletion (source file gone) always deletes `session_usage` rows. On a
    full-commit rewrite, usage-row deletion/replacement executes **only as part of a completed
    scan result** (replace, or completed-empty = delete) — if the usage scan failed, the old
    rows are retained and flagged stale (see below); the purge of messages/tool_calls proceeds
    regardless (same transaction, purge site in ingest.go). [RC-5][RC5-2]
- `quota_snapshots(source TEXT NOT NULL, limit_id TEXT NOT NULL, observed_at INTEGER NOT NULL,
  used_percent REAL, window_minutes INTEGER, resets_at INTEGER, plan_type TEXT, raw_json TEXT,
  PRIMARY KEY(source, limit_id))`. Upsert is timestamp-guarded:
  `ON CONFLICT(source, limit_id) DO UPDATE ... WHERE excluded.observed_at > observed_at` — file
  scan order must never let an older rollout overwrite a newer snapshot.
- `ingest_state` additionally gains a `usage_stale` flag (`INTEGER NOT NULL DEFAULT 0`)
  [RC5-3]: set to 1 when a usage scan fails mid-pass (old aggregate retained but no longer
  known-current), reset to 0 on the next successful scan. doctor reports stale-usage file
  counts; CLI/TUI/MCP session-usage surfaces MUST mark values from stale files as stale rather
  than presenting them as current. Staleness propagates upward [RC6-1]: any grouped row or
  subtotal containing ≥1 stale session is marked stale with its stale-session count (stale
  sessions stay included in totals — exclusion would silently understate).
- `ingest_state` gains `usage_skipped` and `usage_unmapped` columns whose update semantics
  follow the **scope of the extraction pass that produced them** [RC4-2]: whole-file passes
  (Claude usage scan, Gemini replay, any full ingest) **replace** the counters with that pass's
  result — otherwise a Claude scan that re-reads the whole file on every append would re-count
  the same malformed line once per append; only genuinely tail-scoped extraction (Codex
  incremental) accumulates. [RC-5]
  - `usage_skipped`: events/messages that **should** carry usage (Claude assistant messages,
    Codex token_count events, Gemini message records) but are malformed. Messages that simply
    have no usage (user/tool messages) are not counted.
  - `usage_unmapped`: occurrences of category names that fell through to `categories_json` —
    visible drift signal, not an error.

Why not columns on `sessions`: NULL-heavy, couples the hot table to a new feature, and per-model
rows need their own key. Why not per-message rows: rejected by review; no demand evidence.

**D2 — Merge semantics: every source's aggregate is a pure function of complete state; the
only aggregate write operations are replace, delete, and no-op. [RC-1..4][RC3-1][RC3-2][RC3-4]
[RC3-6]**

"No delta bookkeeping" applies to **aggregates** only — the `ingest_state` diagnostic counters
follow the pass-scope semantics defined in D1 (whole-file passes replace, Codex incremental
accumulates). [RC3 P3][RC5-1]

- **Claude — full-file usage scan of the original session file.** [RC3-1] The DB cannot serve
  as complete state (see Context), so on any ingest touching a Claude session, a dedicated
  usage pass re-reads that session's **source file** line by line, extracting only
  (outer uuid, message.model, message.usage) — no FTS work, no message construction. The scan
  covers exactly `[0, newOffset)` — the committed complete-line watermark text ingest itself
  advances to — sharing its complete-line and line-cap semantics; it never reads past the
  watermark into an uncommitted or incomplete tail line. [RC4-3] Dedupe by
  outer uuid, **later line in file wins**; sum per model; the result replaces the session's
  `session_usage` rows. This includes usage on events the message parser excludes (clio-MCP
  tool-use-only events) and reads pre-redaction values, so redaction can never corrupt a
  category. Idempotent by construction.
  - **Transaction placement** [RC3-2]: the file scan runs **outside** the write transaction
    (never extends writer lock hold); the replacement DELETE+INSERT executes **inside** the
    existing commit transaction, after message inserts and before `tx.Commit` (the transaction
    opened at ingest.go:426; note `commit()` has **two** `tx.Commit()` sites — the early-exit
    branch at :455 is the **cross-source conflict** path, which records the conflict and
    rejects the incoming source; on that path usage is a **no-op**: writing usage there would
    pollute the existing source's row for the same session UUID. Usage replacement happens on
    the main path at :529 only. [RC4-1]) The earlier "post-commit" wording is retracted.
  - **Oversized lines are skipped, not fatal** [RC5-4]: matching text ingest's actual
    behavior (skip the over-cap line, count a diagnostic, continue — ingest.go:290), the usage
    scan skips an oversized line, increments `usage_skipped`, and continues; a historical
    oversized line must never permanently block future usage updates. (Known imprecision: an
    over-cap line's role is unknowable without parsing it, so this counts some non-usage lines
    too — conservative over-reporting, accepted.)
  - **Scan failure is atomic no-op, flagged stale** [RC4-4][RC5-3]: if the usage pass hits a
    hard mid-scan error (I/O failure, unreadable file), no `session_usage` aggregate write
    occurs (the `usage_stale` flag itself is written) —
    the session's existing `session_usage` rows are retained unchanged (on full rebuilds too:
    the usage-row purge only executes when a completed scan result — including a completed
    empty result, which means delete — is available), and the file's `usage_stale` flag is set
    so doctor and all surfaces can distinguish retained-old values from current ones. Partial
    scan results MUST never replace or delete anything. If the file grows between scan and commit, the next incremental touch recomputes
    — same eventual-consistency window the text index already has.
  - **Cost**: O(file) per touched session per ingest batch, lifetime O(n²) for a continuously
    appended session — accepted for MVP because the scan is a lean single pass outside the
    lock, and bounded by the tail-ingest gate below. [RC3-3] If the gate fails, the documented
    fallback is per-file usage cursor state — a scoped delta mechanism that would need its own
    design revision, not an ad-hoc patch.
- **Codex — latest cumulative wins; one rollout file = one session; model is always
  `(mixed)`.** [RC-3][RC3-4] `token_count.info.total_token_usage` is cumulative for the whole
  file, so even a tail-only incremental read yields the true whole-file total. A tail cannot
  know which models earlier turns used, so per-model attribution is impossible without a
  full-file model-set scan — MVP therefore always writes the `(mixed)` sentinel and never
  guesses. Three outcomes, explicitly [RC3-6]:
  - **replace**: a `token_count` event was observed in the parsed window → latest cumulative
    value replaces the session's row;
  - **no-op**: an incremental tail contains no `token_count` events → existing row untouched;
  - **delete**: a full rebuild of the file finds no usage events → any stale row for that
    session is deleted.
  `rate_limits` on the same events feed `quota_snapshots` (event ts = `observed_at`).
- **Gemini — replay recompute.** Whole-file replay already recomputes from scratch; sum
  per-message `tokens` during replay and replace (a replay yielding no usage deletes any stale
  row, mirroring Codex full-rebuild semantics). Naturally idempotent.

Backfill: `clio index --full` rebuilds usage for all sessions (release notes must say so).
Sessions without usage rows render a placeholder, and `clio usage`'s empty state names the
command. (The earlier "Claude backfill from DB" optimization idea is dead — the DB is not
complete state.) [RC3-1]

**D3 — Missing/unknown usage shapes degrade, never abort.**
Malformed usage on a should-carry event increments `usage_skipped`; unmapped categories go to
`categories_json` + `usage_unmapped`; both surface in `clio doctor`. Usage extraction failure
never blocks text indexing — usage is an enrichment, text search is the product.

**D4 — Surfaces. [RC-8]**
- CLI: new `clio usage [--since <dur>] [--project <path>] [--source <name>]
  [--by session|project|model]` (a new subcommand, not an `activity` extension — `activity`'s
  existing `--by` values aggregate over extracted activity rows, a different data path).
  - Output is **sectioned per source with per-source subtotals; no cross-source combined total
    is ever printed** — 100k Claude tokens and 100k Gemini tokens are not the same quantity.
  - Jump-through contract applies to session-granularity rows only: `--by session` rows carry
    uuid prefix + title accepted by `clio show`. `--by project|model` rows are aggregates over
    many sessions and instead carry a drill-down hint
    (`clio usage --project <path> --by session`).
  - Subagent sessions appear as their own rows, flagged, consistent with `list`; roll-up into
    parents is a follow-up.
- `clio usage --quota` (CLI-only): humanized staleness ("observed 3h ago, resets in 2d"), fixed
  disclaimer that values are last-observed from session files, not live. A snapshot renders as
  **stale** when `observed_at` is older than its `window_minutes` **or** `resets_at` is in the
  past.
- `clio list` + TUI: per-session total token column; placeholder (not zero) when absent.
- MCP: `activity_summary` gains `group_by: "usage"` returning session-level token aggregates
  (uuid, title, project, source, per-category totals) — session granularity, so the
  jump-through contract holds via `read_session`.

**D5 — Privacy: quota data never crosses MCP. [RC-7]**
No flag, no opt-in, no config subsystem: `quota_snapshots` fields (used_percent, resets_at,
plan_type, credits, and any account-level quota data) do not appear in any MCP tool response in
this change, full stop. Token aggregates are exposed over MCP (they describe content the MCP
client can already read in full). If quota-over-MCP is ever wanted, it arrives as its own change
together with a real config design.

**D6 — `clio index --full` must refuse under MCP lock, loudly. [RC-6]**
Today `clio index` returns "nothing to do" success when the MCP server holds the lock
(cli/index.go:27) — which would silently skip the backfill this feature depends on. Change:
when `--full` is requested and the lock is held, exit non-zero with "MCP server holds the index
lock — stop Claude Code (or the clio MCP server) and re-run". Never report success without
doing the work. (Also fixes a pre-existing footgun; scoped here because backfill makes it acute.)

## Verification Gates (numbers + measurement protocol) [RC-9][RC3-3][RC3-5]

**Protocol** (all gates): fixed, versioned reference fixture set recorded in this change dir;
fresh DB per run; before any size measurement run `PRAGMA wal_checkpoint(TRUNCATE)` and compute
main-DB size as `page_count × page_size`. Timing is two-layered, both interleaved
baseline/candidate on the same machine with medians: **end-to-end CLI metrics** (full-index
wall-clock, CLI tail delta) via fresh-sandbox CLI invocations alternating baseline/candidate
per round, n≥10 — these deliberately include command overhead because that IS the user-facing
quantity; **component metrics** (in-process tail ingest, write-lock hold) via Go benchmarks
with order-counterbalanced (AB/BA) sample pairs. **Hardware contract**: the absolute bounds
(tail < 20 ms) are defined for the reference machine class recorded in
perf/measurements.md (Apple M1-class laptop); certification runs on materially different
hardware must recalibrate by scaling the bound with the measured baseline in-process tail time
(reference: ~2.5 ms) rather than reusing the constant. Numbers are the gate — changing them
requires editing this file, visibly:

- **DB size**: post-checkpoint main-DB growth from usage tables **< 2%** vs baseline on the
  reference set.
- **Full-index throughput**: wall-clock regression **< 5%** (median, interleaved, n≥10).
- **Long-session tail ingest** [RC3-3, re-anchored after in-process measurement]: on the
  5,000-message long-session Claude reference fixture, measured via the in-process Go
  benchmark: the usage pass adds **< 20 ms absolute** to a single tail ingest (measured
  ~11 ms on the reference machine); the CLI-level tail delta is recorded as informational —
  across protocol runs it ranged +21.5%..+40.6%, an unstable metric (real cost + machine noise
  over a denominator of mostly fixed command overhead). The
  original in-process relative bound proved unmeetable by construction: the in-process
  baseline tail ingest is ~3 ms, so ANY whole-file O(file) pass fails a 30% relative bound
  at that granularity — meeting it would require per-uuid cursor state, which is per-message
  bookkeeping by another name (rejected in review round 2). The absolute bound is the
  user-facing truth: watcher events are human-frequency, and +11 ms per append is noise
  there. Write-lock hold change is **one-sided < +10%** (usage making the commit faster is
  not a violation), measured at ≥ 200 iterations × 5 counts (lower settings showed ±12%
  noise). The ~2× fixture is an informational slope point, not a gate; the escalation path
  (per-file usage cursor, own change) triggers on real-world latency complaints.
- **Gemini replay write cost** (redefined measurably [RC3-5]): combined main-DB + WAL file-size
  growth during a single-session re-replay (fresh `wal_checkpoint(TRUNCATE)` before; measure
  file sizes immediately after the replay, before any further checkpoint) grows **< 10%** vs
  baseline.
- doctor DB/source ratio on the reference set stays below the existing 3× warning threshold.

## Risks / Trade-offs

- [Claude O(file) scan per touch, O(n²) lifetime on ever-growing sessions] → lean pass outside
  the lock; tail-ingest gate is the objective check; documented escalation path is a per-file
  usage cursor as its own design revision.
- [Codex `(mixed)`-only reduces per-model insight] → honest by design; per-model Codex
  attribution requires full-file model-set tracking or per-turn accounting — its own change,
  with the sentinel-row deletion rule in D1 as the migration contract.
- [File grows between usage scan and commit] → same eventual-consistency window as text
  indexing; next touch recomputes.
- [Usage fields drift with CLI versions] → D3 degrade path + doctor counters; never abort.
- [Stale snapshots read as current] → mandatory `observed_at` rendering + two stale conditions;
  no surface may print used_percent without its age.
- [Scope creep back toward per-message rows / raw-event persistence / quota-over-MCP / cost
  display] → Non-Goals is the contract; each needs its own change with demand evidence.

## Migration Plan

1. Migration `0011` creates `session_usage` + `quota_snapshots` and adds the three
   `ingest_state` columns (usage_skipped, usage_unmapped, usage_stale) — additive only.
2. Ship extraction + surfaces; existing DBs work immediately with empty usage (placeholder +
   empty-state pointing at `clio index --full`).
3. Rollback = ignore/drop the new tables and columns; no existing behavior depends on them.

## Open Questions

- Whether Codex emits `rate_limits` on all plan types / older CLI versions — capture is
  best-effort; absence degrades to token totals only.
- Exact category name mapping per source (e.g. Gemini `thoughts` → reasoning_tokens) — resolved
  during implementation against real fixtures; unmapped names land in `categories_json` +
  `usage_unmapped`, so a wrong guess is visible, not silent.

# Tasks: add-session-usage-summary

## 1. Schema & fixtures

- [x] 1.1 Migration 0011: `session_usage` (PK session_uuid+model, source/model NOT NULL with
  `(unknown)`/`(mixed)` sentinels, canonical category columns, per-category-summed
  categories_json), `quota_snapshots` (PK source+limit_id, observed_at NOT NULL), and
  `ingest_state` + `usage_skipped`/`usage_unmapped`/`usage_stale` columns — additive only
- [x] 1.2 Real-sample fixtures (from real local files, redacted): Claude session with usage
  incl. duplicated-uuid case, multi-model case, and a clio-MCP-tool-use-only assistant event
  (zero message rows but usage present); Codex rollout with token_count + rate_limits incl.
  no-token-events tail, no-usage-at-all file (delete case), and older-file-after-newer snapshot
  case; Gemini chat with tokens fields; long-session Claude fixture (≥ 5,000 messages) for the
  tail-ingest gate
- [x] 1.3 Baseline measurements per the design.md protocol (fresh DB, wal_checkpoint(TRUNCATE),
  page_count×page_size, interleaved benchmarks n≥10): DB size, full-index wall-clock,
  single tail-ingest wall-clock + write-lock hold on the long-session fixture, Gemini
  single-session re-replay DB+WAL growth — recorded in the change dir for the 5.x gates

## 2. Extraction & aggregation

- [x] 2.1 session_usage write helper with three outcomes (replace = delete session rows +
  insert; delete = complete state yields no usage; no-op) wired into full-commit purge and
  session deletion paths; quota_snapshots timestamp-guarded upsert
  (`WHERE excluded.observed_at > observed_at`)
- [x] 2.2 Claude: dedicated usage pass over the original source file (extract outer uuid /
  message.model / message.usage only; dedupe by uuid, later line wins; group by model), run
  **outside** the write transaction; replacement executed inside the commit transaction after
  message inserts, before Commit — **main path (:529) only; the cross-source-conflict
  early-exit (:455) is a usage no-op** (test: rejected conflicting source leaves existing
  session_usage untouched). Scan covers exactly [0,newOffset) sharing text ingest's
  complete-line/line-cap semantics (test: incomplete tail line not counted). Mid-scan failure
  is an atomic no-op — no replace, no delete — and sets the file's usage_stale flag (test:
  full rebuild with injected read error retains old rows + flag set + cleared on successful
  rescan). Oversized lines are skipped + counted, never fatal (test: historical over-cap line
  doesn't block later usage updates). Fixtures assert: count-once on duplicates, full-session
  totals from a tail-only incremental, and clio-tool-use-only event usage counted
- [x] 2.3 Codex: extract in the currently-skipped token_count branch — latest cumulative per
  file; model always `(mixed)`; three outcomes covered by tests (replace / tail no-op /
  full-rebuild delete); rate_limits → quota_snapshots with event ts as observed_at; malformed
  events increment usage_skipped, never abort
- [x] 2.4 Gemini: sum tokens during whole-file replay (replay with no usage deletes stale row);
  re-index idempotency test; unmapped categories (e.g. thoughts mapping decision) land in
  categories_json + usage_unmapped
- [x] 2.5 `clio index --full` backfill verified on a pre-feature DB (empty → populated); lock
  behavior fixed: `--full` under MCP lock exits non-zero with stop-the-server message, never
  "nothing to do" success
- [x] 2.6 usage_skipped/usage_unmapped counter semantics test: whole-file passes (Claude scan,
  Gemini replay, full ingest) replace; only Codex incremental accumulates; repeated appends to
  a Claude file with one malformed line keep usage_skipped at 1; user/tool messages without
  usage do not count as skipped
- [x] 2.7 total_tokens semantics implemented per spec (native totals for Codex/Gemini, fixed
  derived sum for Claude; categories_json never contributes) with a cross-source fixture test

## 3. Surfaces

- [x] 3.1 `clio usage` subcommand (--since/--project/--source/--by session|project|model):
  per-source sections + subtotals, no cross-source grand total; `--by session` rows carry uuid
  prefix + title accepted by `clio show`; `--by project|model` rows carry drill-down hint;
  subagent sessions flagged as own rows; empty-state points at `clio index --full`; stale
  rendering tests: stale session row marked, grouped/project rows and per-source subtotals
  containing a stale session marked with stale-session count
- [x] 3.2 `clio usage --quota`: staleness rendering (age line; stale when older than window OR
  resets_at in past; fixed "last-observed, not live" disclaimer)
- [x] 3.3 `clio list` + TUI token column (placeholder when no data, not zero; stale marking
  test in both)
- [x] 3.4 MCP activity_summary group_by="usage": session-level aggregates with source
  attribution, identifiers accepted by read_session; stale field on stale sessions (test);
  test asserts no quota field appears in any MCP response (no flag exists)

## 4. Diagnostics & docs

- [x] 4.1 doctor: report usage coverage (sessions with usage / total),
  usage_skipped/usage_unmapped counts, and stale-usage file count (usage_stale flags)
- [x] 4.2 Fix stale debt comments found in review: 16MiB wording (gemini.go — abort not silent
  truncation) and claudeconfig atomic-write comment (remaining debt is backup-failure semantics)
- [x] 4.3 README/USAGE docs: usage command, per-source non-comparability note, quota staleness
  semantics + CLI-only stance, "full reindex required for backfill" release note draft

## 5. Verification gates (ship gate — all must pass before archive; numbers + protocol are the
contract, defined in design.md)

- [x] 5.1 Post-checkpoint DB growth vs 1.3 baseline < 2%; doctor DB/source ratio stays below
  the 3× warning
- [x] 5.2 Full-index wall-clock regression < 5% (interleaved, n≥10, medians)
- [x] 5.3 Long-session tail-ingest gate: usage pass adds < 30% wall-clock, write-lock hold
  change < 10%, scanned bytes/rows recorded; additionally record (informational, non-gating)
  the same measurement on a ~2× larger fixture to document the cost slope
- [x] 5.4 Gemini single-session re-replay combined DB+WAL growth < 10%
- [x] 5.5 Grep gate: no monetary amount stored, computed, or displayed anywhere in this change
- [x] 5.6 Fresh-context verifier (7/7 PASS, 42-scenario mapping) + codex adversarial review
  over the implementation: 4 rounds to CLEAN GATE (round 1: 6 FIX areas incl. tombstone
  last-wins P1; round 2: surfaces/tests/perf-shape; round 3: measurement protocol + gate
  re-anchor judged sound; round 4: CLEAN GATE, no new P1/P2)

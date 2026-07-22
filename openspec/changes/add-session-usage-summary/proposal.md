# Proposal: add-session-usage-summary

## Why

clio already indexes the exact JSONL files that carry token usage for all three sources
(Claude Code per-message `message.usage`, Codex cumulative `token_count` events, Gemini
per-message `tokens` fields), but
extracts none of it — users cannot answer "which sessions burned my tokens, and what was I doing
in them?" without a second tool. This proposal is the revised scope after two rounds of
adversarial cross-model review (codex gpt-5.6-sol; second round 2026-07-21 GO-WITH-CHANGES ×9,
third round 2026-07-22 GO-WITH-CHANGES ×6 — all fifteen required changes folded in): the MVP is a **session-level usage
summary with content jump-through**, deliberately not per-message usage rows. Demand beyond the
session-level summary is treated as unproven: this MVP is itself the demand probe, and
per-message granularity requires its own change with usage evidence.

## What Changes

- Ingest extracts token usage from all three source parsers and aggregates it to **session-level
  rows** (keyed session+model, source-tagged, raw token categories only). Each source produces a
  deterministic aggregate from complete state (Claude: dedicated usage pass over the original
  source file — the filtered messages table is not complete state — deduped by uuid, later line
  wins; Codex: latest cumulative counter, one rollout file = one session, model always
  `(mixed)`; Gemini: replay recompute). Aggregate writes are replace/delete/no-op only; no
  per-message usage rows, no delta bookkeeping for aggregates.
- Codex `rate_limits` payloads become **quota snapshots** with mandatory `observed_at`,
  timestamp-guarded upserts, and staleness rendering (stale when older than window or past
  `resets_at`); never presented as live. Claude Code and Gemini quota capture is explicitly
  **out of scope** (Claude needs statusline integration — the existing SessionStart hook cannot
  see statusline stdin; Gemini needs a quota API design). clio does not claim "unified quota
  view across three CLIs".
- CLI: new `clio usage` subcommand (top sessions/projects/models by tokens, per-source sections,
  no cross-source totals, jump-through at session granularity, drill-down hints for aggregate
  groupings, `--quota` for snapshots); `clio list` and the TUI show per-session token totals.
- MCP: `activity_summary` gains a session-level usage dimension (token aggregates only).
  **Quota data never crosses MCP in this change** — no flag, no config subsystem.
- No monetary cost anywhere: raw token category counts only; nothing stores, computes, or
  displays a dollar amount in this change's scope.
- `clio index --full` refuses loudly (non-zero exit) when the MCP server holds the index lock,
  instead of today's silent "nothing to do" success — backfill depends on it.
- Stale `debt:` comments identified during review (16MiB "silent truncation" wording,
  claudeconfig atomic-write) are corrected in passing.

## Capabilities

### New Capabilities

- `usage-summary`: session-level token usage aggregation (deterministic replacement semantics,
  storage shape, raw-categories-not-dollars invariant), quota snapshot capture with staleness
  semantics, degrade-not-abort diagnostics, and the session-granularity jump-through contract.

### Modified Capabilities

- `session-ingest`: parsers additionally extract usage/token events they currently skip
  (Codex `token_count` outer events are dropped entirely today; Claude/Gemini token fields are
  parsed-and-ignored). Requirement changes: per-source deterministic aggregates, per-file usage
  diagnostic counters in `ingest_state`, numeric growth/throughput gates, and the
  `index --full` lock-refusal behavior.
- `cli-surface`: new `usage` subcommand with per-source sectioning and drill-down semantics;
  `list`/TUI token column.
- `mcp-server`: `activity_summary` usage dimension (token aggregates only); quota data
  categorically excluded from MCP responses.

## Impact

- Code: `internal/ingest/` (parser.go, codex.go, gemini.go, ingest.go commit/purge paths), DB
  migration 0011 (`session_usage`, `quota_snapshots`, three `ingest_state` columns),
  `internal/db/`, `internal/cli/` (new usage.go, list, index lock behavior), `internal/tui/`,
  `internal/mcp/` (activity_summary), `internal/doctor/` (usage coverage + counters).
- Data: one additive migration; `clio index --full` required to backfill usage for existing
  sessions — release notes must say so.
- Performance gates (numeric, on the reference fixture set, under the measurement protocol in
  design.md): post-checkpoint DB growth < 2%; full-index wall-clock regression < 5%;
  long-session tail-ingest overhead < 30% with write-lock hold change < 10%; Gemini re-replay
  combined DB+WAL growth < 10%; doctor DB/source ratio stays below the 3× warning.
- Non-goals (recorded so scope creep is visible): per-message usage rows, any monetary cost,
  live quota polling, Claude statusline integration, Gemini quota API, quota over MCP, a config
  subsystem, cross-source combined totals, subagent usage roll-up, any standalone usage
  dashboard product.

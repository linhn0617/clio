# usage-summary Specification

## Purpose
TBD - created by archiving change add-session-usage-summary. Update Purpose after archive.
## Requirements
### Requirement: Session-level usage aggregation
The system SHALL store token usage as aggregate rows in a `session_usage` table keyed
(session_uuid, model) with `source` and `model` NOT NULL, canonical category columns (input,
output, cache_read, cache_creation, reasoning, tool, total), and a `categories_json` field
holding per-category **summed** values for source-specific categories. The system MUST NOT
create per-message usage rows or persist per-event raw usage records. Per-model rows SHALL
exist only when the source attributes usage to a model; otherwise the row uses the `(unknown)`
or `(mixed)` sentinel. A future per-model-attribution change MUST delete a session's sentinel
row before inserting named-model rows, so sentinel and named rows are never summed together.

#### Scenario: Aggregate written on ingest
- **WHEN** a session containing token usage events is indexed
- **THEN** `session_usage` rows exist for that session whose totals match the source file's
  usage data, keyed by attributed model or a sentinel

#### Scenario: Claude multi-model session
- **WHEN** a Claude session contains assistant messages attributed to two different models
- **THEN** two rows exist for that session_uuid, one per model, and their totals are not merged

#### Scenario: Codex rows use the mixed sentinel
- **WHEN** a Codex rollout's usage is aggregated (its cumulative counter carries no per-model
  attribution)
- **THEN** one row exists with model `(mixed)` and no fabricated per-model split

### Requirement: Aggregates are deterministic replacements
Each source's usage aggregate SHALL be a deterministic function of complete state — Claude:
recomputed by a dedicated usage pass over the session's **original source file** (not the
filtered messages table), deduplicated by outer message uuid with the later line in the file
winning; Codex: the latest observed cumulative `total_token_usage` of the session's single
rollout file; Gemini: recomputed during whole-file replay. Aggregate writes SHALL be limited to
replace, delete (when complete state yields no usage), and no-op (Codex tail without token
events); no delta accumulation is stored for aggregates (per-file diagnostic counters follow
their pass-scope semantics: whole-file passes replace, Codex incremental accumulates).

#### Scenario: Repeated ingest is idempotent
- **WHEN** the same session is indexed twice (incremental tail or full)
- **THEN** its `session_usage` rows are identical after both runs

#### Scenario: Session deletion removes usage
- **WHEN** a session's source file is deleted, or a full-commit rewrite completes with a
  completed usage scan result
- **THEN** its previous `session_usage` rows are deleted (or replaced) in the same transaction

### Requirement: Stale usage is distinguishable from current
A failed usage scan SHALL set a per-file `usage_stale` flag (cleared on the next successful
scan). Doctor SHALL report stale-usage file counts, and every surface presenting session usage
(CLI, TUI, MCP) SHALL mark values from stale files as stale rather than presenting retained old
totals as current.

Staleness SHALL propagate to aggregates: any grouped row or subtotal (project, model,
per-source) that includes at least one stale session SHALL be marked stale and carry the count
of stale sessions it contains; stale sessions are included in (not excluded from) the totals.

#### Scenario: Retained totals marked stale
- **WHEN** a usage scan fails and the previous aggregate is retained
- **THEN** `clio usage` and MCP responses mark that session's usage as stale until a successful
  rescan clears the flag

#### Scenario: Stale propagates to grouped rows
- **WHEN** `clio usage --by project` aggregates a project containing one stale session
- **THEN** that project row is marked stale with its stale-session count, and the per-source
  subtotal covering it carries the same marking

### Requirement: total_tokens is reproducibly defined per source
The `total_tokens` column SHALL store the source's native total when one exists (Codex
`total_token_usage.total_tokens`, Gemini `tokens.total`) and otherwise the fixed derived sum
input + output + cache_read + cache_creation (Claude). Categories present only in
`categories_json` SHALL NOT contribute to `total_tokens`. All ranking surfaces SHALL order by
this column.

#### Scenario: Claude derived total is stable
- **WHEN** a Claude session's usage is aggregated twice from the same file state
- **THEN** `total_tokens` equals input + output + cache_read + cache_creation both times

#### Scenario: Native totals win
- **WHEN** a Codex or Gemini record carries a native total
- **THEN** `total_tokens` stores the native value, not a re-derived sum

### Requirement: No persisted or displayed monetary cost
The system SHALL persist only raw token category counts. No dollar or currency amount SHALL be
stored, computed, or displayed by any surface in this change's scope.

#### Scenario: No cost anywhere
- **WHEN** the schemas and all usage surfaces (CLI, TUI, MCP) are inspected
- **THEN** no monetary amount is stored or rendered

### Requirement: Quota snapshots carry staleness
The system SHALL store Codex `rate_limits` payloads as snapshots keyed (source, limit_id) with
mandatory `observed_at`. Upserts MUST be timestamp-guarded so an older observation never
overwrites a newer one regardless of file scan order. Every surface rendering a snapshot MUST
display its age and MUST NOT present it as live; a snapshot SHALL render as stale when
`observed_at` is older than `window_minutes` OR `resets_at` is in the past.

#### Scenario: Snapshot rendered with age
- **WHEN** `clio usage --quota` prints a snapshot observed 3 hours ago
- **THEN** the output includes the observation age and a disclaimer that the value is
  last-observed from session files, not live

#### Scenario: Past reset renders stale
- **WHEN** a snapshot's `resets_at` is in the past
- **THEN** the surface renders it as stale instead of showing only the percentage

#### Scenario: Older file cannot overwrite newer snapshot
- **WHEN** ingest processes an older rollout after a newer one for the same (source, limit_id)
- **THEN** the stored snapshot still carries the newer `observed_at`

### Requirement: Usage extraction degrades without blocking indexing
Usage extraction SHALL degrade without blocking indexing — it is an enrichment: an event that
should carry usage but is malformed increments
a per-file `usage_skipped` counter; category names outside the canonical mapping are preserved
in `categories_json` and counted in `usage_unmapped`. Both counters persist in `ingest_state`
and surface in `clio doctor`. Messages that legitimately carry no usage SHALL NOT be counted.
Usage extraction failures MUST NOT abort or skip text indexing.

#### Scenario: Malformed usage event
- **WHEN** a session file contains a token event whose shape does not match the known format
- **THEN** the session's text is still fully indexed and `clio doctor` reports the skipped count

#### Scenario: Non-usage messages not miscounted
- **WHEN** a session contains ordinary user and tool messages without usage fields
- **THEN** `usage_skipped` does not increase

### Requirement: Session-level rows link to content
Every session-granularity usage row SHALL identify the session such that the user or MCP client
can open the underlying conversation (uuid or uuid prefix plus title). Aggregate rows above
session granularity SHALL carry a drill-down path to a session-level listing instead.

#### Scenario: Jump-through from session listing
- **WHEN** `clio usage --by session` lists the top sessions by tokens
- **THEN** each row shows a session identifier accepted by `clio show`


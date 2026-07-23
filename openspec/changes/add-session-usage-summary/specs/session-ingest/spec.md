# session-ingest (delta)

## ADDED Requirements

### Requirement: Parsers extract token usage per source
Ingest SHALL extract token usage during normal indexing for all three sources, each producing a
deterministic aggregate from complete state: Claude Code runs a dedicated usage pass over the
session's **original source file** (extracting only outer uuid, `message.model`, and
`message.usage`; deduplicated by uuid with the later line winning; grouped by model) — the
filtered `messages` table MUST NOT be used as the usage source, because the parser excludes
clio-MCP tool-use events that still carry usage; Codex takes the latest cumulative
`total_token_usage` observed in the session's rollout file (one rollout file is one session —
no cross-file summing) and always writes the `(mixed)` model sentinel; Gemini sums per-message
`tokens` during whole-file replay. The Claude usage scan SHALL run outside the write
transaction, SHALL cover exactly the committed complete-line watermark `[0, newOffset)` with
the same complete-line and line-cap semantics as text ingest, and the aggregate replacement
SHALL execute inside the commit transaction after message inserts and before commit — on the
main commit path only: the cross-source-conflict early-exit path SHALL be a usage no-op. A
usage scan that fails mid-pass SHALL be an atomic no-op: no replace, no delete, existing rows
retained. Codex `rate_limits` payloads SHALL be captured into quota
snapshots with the event timestamp as `observed_at` under the timestamp-guarded upsert.

#### Scenario: Claude duplicate uuids count once
- **WHEN** a Claude session file contains the same message uuid on multiple JSONL lines
- **THEN** the session's usage aggregate counts that uuid's usage exactly once, using the later
  line's values

#### Scenario: Claude usage on parser-excluded events still counted
- **WHEN** a Claude session contains an assistant event whose only content block is a clio MCP
  tool call (which produces zero message rows in the text index)
- **THEN** that event's `message.usage` is still included in the session's aggregate

#### Scenario: Claude incremental tail yields full-session totals
- **WHEN** a pre-existing indexed Claude session receives new messages via incremental ingest
- **THEN** the recomputed aggregate covers the entire source file, not only the new tail

#### Scenario: Codex cumulative counter not double-summed
- **WHEN** a Codex rollout contains successive `token_count` events with growing cumulative
  totals
- **THEN** the aggregate equals the latest cumulative value, not the sum of all events

#### Scenario: Codex tail without token events is a no-op
- **WHEN** an incremental read of a Codex rollout tail contains no `token_count` events
- **THEN** the session's existing usage row is left untouched

#### Scenario: Full rebuild without usage deletes stale row
- **WHEN** a full rebuild of a session file finds no usage events but a stale usage row exists
- **THEN** the stale row is deleted

#### Scenario: Gemini replay idempotent
- **WHEN** the same Gemini session is re-indexed twice via whole-file replay
- **THEN** the usage aggregate is identical after both runs

#### Scenario: Cross-source conflict leaves usage untouched
- **WHEN** a file from a different source carrying usage is rejected as a session-UUID
  conflict (the early-exit commit path)
- **THEN** the existing session's `session_usage` rows are completely unchanged

#### Scenario: Uncommitted tail line not counted
- **WHEN** a Claude session file ends in an incomplete line beyond the committed watermark
- **THEN** the usage scan does not count it; it is picked up once text ingest commits it

#### Scenario: Failed scan is an atomic no-op flagged stale
- **WHEN** the usage pass encounters a hard mid-scan read error during a full rebuild
- **THEN** the session's previous usage rows are retained — neither partially replaced nor
  deleted — and the file's `usage_stale` flag is set until a successful rescan

#### Scenario: Oversized line skipped, scan continues
- **WHEN** a Claude session file contains a historical line exceeding the line cap
- **THEN** the usage scan skips it, counts it in `usage_skipped`, and later appends to the
  same file still update the usage aggregate

### Requirement: Full reindex backfills usage
`clio index --full` SHALL rebuild `session_usage` for all existing sessions. Incremental ingest
SHALL keep aggregates current for sessions it touches. When `--full` is requested while the MCP
server holds the index lock, the command SHALL exit non-zero with a message naming the lock
holder; it MUST NOT report success without doing the work.

#### Scenario: Backfill on full reindex
- **WHEN** a user upgrades and runs `clio index --full`
- **THEN** sessions indexed before the upgrade have usage aggregates

#### Scenario: Locked full reindex refuses loudly
- **WHEN** `clio index --full` runs while the MCP server holds the index lock
- **THEN** the command exits non-zero and instructs the user to stop the MCP server, and no
  "success/nothing to do" is reported

### Requirement: Usage diagnostics persist per file
`ingest_state` SHALL gain `usage_skipped` and `usage_unmapped` counters whose update semantics
follow the scope of the extraction pass: whole-file passes (Claude usage scan, Gemini replay,
any full ingest) SHALL replace the counters with that pass's result; only tail-scoped
extraction (Codex incremental) accumulates. Surfaced by `clio doctor` alongside
`unparsed_lines`.

#### Scenario: Whole-file pass replaces counters
- **WHEN** a Claude file containing one malformed usage line receives three successive
  incremental appends (each triggering a whole-file usage scan)
- **THEN** `usage_skipped` remains 1, not 3

#### Scenario: Full re-ingest replaces counters
- **WHEN** a file with malformed usage events is fully re-ingested
- **THEN** its `usage_skipped` value reflects that run alone (replace, not accumulate)

### Requirement: Bounded storage growth and ingest cost
The usage tables SHALL add O(session-model pairs) rows only (bounded by a small constant
factor over sessions in practice). All gates below are measured under the
protocol in design.md (versioned fixture set, fresh DB, `wal_checkpoint(TRUNCATE)` before size
reads, main-DB size as page_count × page_size; two timing layers per design.md — end-to-end
CLI metrics via interleaved fresh-sandbox CLI invocations n≥10, component metrics via
order-counterbalanced Go benchmarks — comparing medians; absolute bounds are defined for the reference hardware class recorded in
perf/measurements.md and MUST be recalibrated proportionally to the measured baseline
in-process tail time on materially different hardware). On the reference fixture set:
post-checkpoint DB growth MUST be < 2%; full-index
wall-clock regression MUST be < 5%; on the 5,000-message long-session Claude reference fixture
the usage pass MUST add < 20 ms absolute to a single in-process tail ingest (the CLI-level
tail delta is recorded as informational — it proved an unstable metric, ~11-15 ms of real cost
plus machine noise over a denominator of mostly fixed command overhead), with write-lock hold
time increase < 10% one-sided measured in alternated pairs (the ~2× fixture
is an informational slope point, not a gate — the scan is O(file) per touch by design, and the
documented escalation path for longer sessions is a per-file usage cursor as its own change;
an in-process RELATIVE bound is unmeetable by construction over a ~3 ms baseline, see design);
Gemini single-session re-replay combined DB+WAL file-size growth MUST be < 10%; and the
doctor DB/source ratio MUST stay below the existing 3× warning threshold.

#### Scenario: Growth and throughput measured against numeric gates
- **WHEN** the reference fixture set is indexed with and without usage extraction under the
  measurement protocol
- **THEN** the recorded deltas are documented in the change dir and each is within its numeric
  gate above

#### Scenario: Long-session tail ingest stays within its gate
- **WHEN** a single incremental tail is ingested into the long-session Claude fixture with the
  usage pass enabled
- **THEN** the in-process absolute overhead is < 20 ms and write-lock hold time increase is
  < 10% (one-sided, alternated pairs), with the CLI-level delta and scanned bytes/rows recorded

# raw-json-retention Specification

## Purpose
TBD - created by archiving change add-raw-json-retention. Update Purpose after archive.
## Requirements
### Requirement: prune-raw blanks only restorable old raw_json, transactionally
The CLI SHALL provide `clio prune-raw --older-than <dur> [--dry-run] [--vacuum]
[--source <name>] [--project <prefix>]` that blanks `messages.raw_json` (to the
empty pruned sentinel) for messages of eligible sessions in a single write
transaction. `--older-than` reuses the since-grammar (`14d`/`12h`/`YYYY-MM-DD`)
and MUST reject zero/negative/malformed values. A session is eligible iff ALL of:
its most recent activity (`COALESCE(ended_at, started_at, 0)`) is strictly older
than the cutoff; its `source_file` is a regular, readable file; it lies under any
currently-scanned source root; and its `ingest_state` row is a completed current ingest
(`last_size==fi.Size()`, `last_mtime==fi.ModTime()`, `last_byte_offset==last_size`,
AND `aborted==0` — offset alone is insufficient because an aborted pass preserves
a prior offset on conflict; migration 0012's `aborted` flag (existing rows
initialized 1, fail-closed) makes successful completion independently
observable, so a session is prunable only after a successful full reindex
validates it). The UPDATE SHALL carry a
`raw_json <> ''` guard (idempotent reruns; counts = newly-pruned rows).
`content`/FTS postings, `session_usage`, quota snapshots, and `tool_targets`
SHALL be byte-identical after pruning.

#### Scenario: Old restorable session is pruned without FTS churn
- **WHEN** `clio prune-raw --older-than 14d` prunes an old session whose source
  file is present, readable, and under a scanned root
- **THEN** its messages have `raw_json` empty, and its `content` rows and FTS
  postings are unchanged (a subsequent `clio doctor` FTS integrity-check passes)

#### Scenario: Recent session is not pruned
- **WHEN** a session's most recent activity is within the cutoff
- **THEN** its `raw_json` is left intact

#### Scenario: Non-restorable sessions are skipped and counted by reason
- **WHEN** an old session's source file is missing, unreadable, outside any
  scanned root, its ingest snapshot is lagged, or its ingest is unverified
  (`aborted=1`, e.g. a row freshly migrated from before 0012 with no subsequent
  `index --full`)
- **THEN** its `raw_json` is NOT blanked and it is reported under the matching
  skip reason (missing/unreadable/undiscoverable/lagged/unverified)

#### Scenario: Rerun is idempotent
- **WHEN** `clio prune-raw --older-than 14d` runs twice
- **THEN** the second run prunes zero additional rows and reports zero
  newly-pruned

#### Scenario: Malformed duration is rejected
- **WHEN** `--older-than` is zero, negative, or unparseable
- **THEN** the command exits non-zero without modifying the database

### Requirement: pending activity backfill is never stranded (read-only skip)
The command SHALL NOT run `BackfillActivity` (that writes `tool_targets`,
breaking dry-run and byte-identical guarantees). It SHALL instead exclude,
read-only, any claude-code session that still has tool_use messages missing
`tool_targets`, so no session is pruned while a backfill needing its `raw_json`
is pending; such sessions are skipped and counted.

#### Scenario: Session with pending target extraction is not pruned
- **WHEN** a claude-code session still has tool_use messages without
  `tool_targets` at prune time
- **THEN** that session is skipped (and `tool_targets` is unchanged — the
  command performs no backfill), until normal ingest backfills it

#### Scenario: prune-raw writes nothing but raw_json
- **WHEN** any `clio prune-raw` run completes
- **THEN** the only column it has modified is `messages.raw_json`
  (`tool_targets`, `content`, FTS, `session_usage` untouched)

### Requirement: dry-run reports without writing
With `--dry-run`, the command SHALL report the eligible session count, message
count, and skipped-by-reason counts, and SHALL NOT modify the database.

#### Scenario: Dry run makes no changes
- **WHEN** `clio prune-raw --older-than 30d --dry-run` runs
- **THEN** it prints the counts and no `raw_json` value changes

### Requirement: pruned raw_json is restorable and restore failure is visible
Pruning SHALL be reversible for every eligible (restorable) session: a
subsequent `clio index --full` re-derives and re-stores `raw_json` from the
source file (Claude re-parse, Codex rollout, Gemini whole-file replay). Because
full reindex logs per-file failures and continues, `clio index --full` SHALL,
after a full run, report and exit non-zero if any session still has pruned
`raw_json`, so an unrestored prune is never silent. The prune command SHALL
print the restore path on completion.

#### Scenario: Full reindex restores pruned raw_json for all three sources
- **WHEN** Claude, Codex, and Gemini sessions are each pruned and then `clio
  index --full` runs with their source files present
- **THEN** each session's `raw_json` is repopulated

#### Scenario: Unrestored prune is surfaced
- **WHEN** after `clio index --full` some pruned session was not restored (e.g.
  its file became unreadable)
- **THEN** the command reports that session and exits non-zero

### Requirement: space is reclaimed on VACUUM; --vacuum is lock-checked first
The command SHALL reclaim file space only via VACUUM (blanking `raw_json` moves
freed pages to the freelist; the file shrinks only on VACUUM). When `--vacuum`
is requested the command SHALL check the MCP index lock **before** any mutation
and, if held, exit non-zero making NO database change (no prune, no vacuum).
With the lock free it SHALL prune then VACUUM. Without `--vacuum` it SHALL prune
and print the exact logical raw bytes removed
(`SUM(length(CAST(raw_json AS BLOB)))` — true UTF-8 bytes — over the pruned
rows, computed before blanking) and how to VACUUM to reclaim on disk.

#### Scenario: Vacuum after prune shrinks the file
- **WHEN** `clio prune-raw --older-than 30d --vacuum` runs with the index lock
  free
- **THEN** the file size decreases and no FTS/content/usage data changes

#### Scenario: Vacuum refusal under MCP lock is a full no-op
- **WHEN** `--vacuum` is requested while the MCP server holds the index lock
- **THEN** the command exits non-zero, having neither pruned nor vacuumed

#### Scenario: dry-run takes precedence over vacuum
- **WHEN** `clio prune-raw --older-than 30d --dry-run --vacuum` runs
- **THEN** it performs no lock check, no prune, and no VACUUM — only reporting
  the would-remove bytes and skipped-by-reason counts


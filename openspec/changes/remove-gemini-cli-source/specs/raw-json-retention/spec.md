## MODIFIED Requirements

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
AND `aborted==0` — migration 0012's `aborted` flag (existing rows initialized 1,
fail-closed) makes successful completion independently observable, so a
session is prunable only after a successful full reindex validates it; no
current source produces an aborted pass at runtime, the flag remains as the
fail-closed default for pre-0012 rows). The UPDATE SHALL carry a
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


### Requirement: pruned raw_json is restorable and restore failure is visible
Pruning SHALL be reversible for every eligible (restorable) session: a
subsequent `clio index --full` re-derives and re-stores `raw_json` from the
source file (Claude re-parse, Codex rollout). Because
full reindex logs per-file failures and continues, `clio index --full` SHALL,
after a full run, report and exit non-zero if any session still has pruned
`raw_json`, so an unrestored prune is never silent. The prune command SHALL
print the restore path on completion.

#### Scenario: Full reindex restores pruned raw_json for both sources
- **WHEN** Claude and Codex sessions are each pruned and then `clio
  index --full` runs with their source files present
- **THEN** each session's `raw_json` is repopulated

#### Scenario: Unrestored prune is surfaced
- **WHEN** after `clio index --full` some pruned session was not restored (e.g.
  its file became unreadable)
- **THEN** the command reports that session and exits non-zero

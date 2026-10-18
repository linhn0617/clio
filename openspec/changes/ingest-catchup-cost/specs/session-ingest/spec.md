## MODIFIED Requirements

### Requirement: One-time activity backfill

The system SHALL populate `tool_targets` for already-indexed messages from their
stored (redacted) `raw_json`, without re-reading source files. The backfill SHALL
be idempotent and run automatically when the table is empty. The incremental catch-up
that read-style CLI commands run before answering SHALL run the backfill only when that
catch-up ingested at least one file; `clio index` (with or without `--full`) SHALL run it
on every invocation.

#### Scenario: Existing history is backfilled

- **WHEN** the activity index is added to a database that already contains
  tool_use messages
- **THEN** activity queries SHALL return facts for those historical messages
  without requiring a full re-ingest

#### Scenario: Read-path catch-up with nothing new skips the backfill

- **WHEN** a read-style CLI command's catch-up ingests no file
- **THEN** it SHALL NOT run the activity backfill pre-check

#### Scenario: Read-path catch-up with new content runs the backfill

- **WHEN** a read-style CLI command's catch-up ingests at least one file
- **THEN** it SHALL run the activity backfill

#### Scenario: Index command always backfills

- **WHEN** the user runs `clio index` or `clio index --full` and nothing new is ingested
- **THEN** the activity backfill SHALL still run once

## ADDED Requirements

### Requirement: No redundant messages session index

The schema SHALL NOT keep a single-column index on `messages(session_uuid)` alongside the
unique `(session_uuid, seq)` index, whose prefix serves every lookup by session.

#### Scenario: Migration removes the redundant index

- **WHEN** migrations run on a database that still has `idx_messages_session`
- **THEN** that index SHALL be dropped and queries filtering `messages` by `session_uuid`
  SHALL plan onto `idx_messages_session_seq`

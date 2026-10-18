## MODIFIED Requirements

### Requirement: One-time activity backfill

The system SHALL populate missing `tool_targets` for already-indexed Claude Code
messages (including NULL-source sessions) from stored redacted `raw_json` without
re-reading source files. Every read-style CLI catch-up and every `clio index`
invocation SHALL call the idempotent backfill regardless of files ingested.
An unseeded singleton state table SHALL persist the highest message ID examined;
an absent row SHALL mean zero. The pending check SHALL use a messages primary-key
range above that watermark. Backfill SHALL preserve per-event block alignment even
when the watermark splits an event. Targets and progress SHALL commit together;
errors SHALL leave progress unchanged. Examined unfillable rows SHALL advance
progress. With no pending targets, progress SHALL advance to the snapshot maximum
message ID if larger, using a monotonic write safe under concurrent processes.
When the maximum ID equals the watermark, backfill SHALL perform no write.

#### Scenario: Upgrade with unchanged files

- **WHEN** an upgraded database has historical tool_use messages without targets and no new files
- **THEN** the first read-path catch-up SHALL backfill those messages and persist progress

#### Scenario: Unfillable message is examined once

- **WHEN** a message produces no targets
- **THEN** its ID SHALL still be covered by the committed watermark and subsequent calls SHALL skip it

#### Scenario: Failure retries

- **WHEN** backfill fails after inserting some targets
- **THEN** targets and progress SHALL roll back and the next invocation SHALL retry

#### Scenario: New messages after progress

- **WHEN** messages are added after a successful backfill
- **THEN** only IDs above the watermark SHALL be examined for missing targets
- **AND** preceding blocks needed for event alignment SHALL not receive duplicate targets

#### Scenario: Caught-up read

- **WHEN** the highest message ID equals the watermark
- **THEN** backfill SHALL use a primary-key range check and SHALL perform no write

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

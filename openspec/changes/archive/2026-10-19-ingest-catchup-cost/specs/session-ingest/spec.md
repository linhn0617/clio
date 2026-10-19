## MODIFIED Requirements

### Requirement: One-time activity backfill

The system SHALL populate missing `tool_targets` for already-indexed Claude Code
messages (including NULL-source sessions) from stored redacted `raw_json` without
re-reading source files. Every read-style CLI catch-up and every `clio index`
invocation SHALL call the idempotent backfill regardless of files ingested.
An unseeded singleton state table SHALL persist the highest message ID examined;
an absent row SHALL mean zero. The read-path pending check SHALL use a messages primary-key
range above that watermark. Both incremental `clio index` and `clio index --full`
SHALL ignore the watermark and run a full-history missing-target pre-check and
repair pass. Backfill SHALL preserve per-event block alignment even
when the watermark splits an event. Targets and progress SHALL commit together;
transaction errors SHALL leave progress unchanged. Examined unfillable rows SHALL advance
progress. With no pending targets, progress SHOULD advance to the snapshot maximum
message ID if larger, using a monotonic best-effort write safe under concurrent
processes. On the read path this attempt SHALL NOT wait for a busy writer or
produce a warning; correctness SHALL NOT depend on its success.
When the maximum ID equals the watermark, backfill SHALL perform no write.

#### Scenario: Existing history is backfilled

- **WHEN** the activity index is added to a database that already contains
  tool_use messages
- **THEN** activity queries SHALL return facts for those historical messages
  without requiring a full re-ingest

#### Scenario: Upgrade with unchanged files

- **WHEN** an upgraded database has historical tool_use messages without targets and no new files
- **THEN** the first read-path catch-up SHALL backfill those messages and persist progress

#### Scenario: Unfillable message is examined once

- **WHEN** a message produces no targets
- **THEN** its ID SHALL still be covered by the committed watermark and subsequent read-path calls SHALL skip it

#### Scenario: Failure retries

- **WHEN** backfill fails after inserting some targets
- **THEN** targets and progress SHALL roll back and the next invocation SHALL retry

#### Scenario: New messages after progress

- **WHEN** messages are added after a successful backfill
- **THEN** read-path backfill SHALL examine only IDs above the watermark for missing targets
- **AND** preceding blocks needed for event alignment SHALL not receive duplicate targets

#### Scenario: Caught-up read

- **WHEN** the highest message ID equals the watermark
- **THEN** backfill SHALL use a primary-key range check and SHALL perform no write

#### Scenario: Index command always backfills

- **WHEN** the user runs `clio index` or `clio index --full` and nothing new is ingested
- **THEN** a full-history repair SHALL still run once regardless of the watermark
- **AND** fillable messages at or below the watermark missing targets SHALL be repaired

#### Scenario: Busy writer during no-pending advancement

- **WHEN** new IDs need no target extraction and another connection holds a write transaction
- **THEN** read-path backfill SHALL return promptly without a warning
- **AND** leaving the watermark unchanged SHALL be safe for a later retry

## ADDED Requirements

### Requirement: Shared pending activity definition for pruning

Automatic backfill and both prune-raw guards SHALL share the definition of pending:
Claude Code tool-use messages (including NULL-source sessions) above the examined
watermark without targets. Pending queries SHALL use the primary-key range rather
than scan historical messages. Preflight and in-transaction prune validation SHALL
protect raw_json still needed by future automatic backfill. Examined unfillable
messages SHALL NOT block pruning; explicit repair after pruning requires source
restoration with `clio index --full`.

#### Scenario: Examined unfillable history

- **WHEN** automatic backfill examines a message but cannot extract targets
- **THEN** neither prune guard SHALL consider it pending once its watermark commits

#### Scenario: Unexamined message needs raw_json

- **WHEN** a Claude Code tool message above the watermark has no targets
- **THEN** both prune guards SHALL exclude its session from pruning
- **AND** a later backfill SHALL still have its raw_json available


### Requirement: No redundant messages session index

The schema SHALL NOT keep a single-column index on `messages(session_uuid)` alongside the
unique `(session_uuid, seq)` index, whose prefix serves every lookup by session.

#### Scenario: Migration removes the redundant index

- **WHEN** migrations run on a database that still has `idx_messages_session`
- **THEN** that index SHALL be dropped and queries filtering `messages` by `session_uuid`
  SHALL plan onto `idx_messages_session_seq`

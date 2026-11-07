## ADDED Requirements

### Requirement: Every ingest path applies the raw retention window
The write-time raw_json drop of the raw-json-retention capability SHALL apply to every
ingest path — `clio index`, `clio index --full`, the CLI read catch-up, the MCP
leader's startup catch-up and read catch-up, the watcher, and `install-mcp` — and to
every source (Claude Code, Codex). The setting SHALL be read once per `IngestAll` or
`IngestFile` call. The retention sweep SHALL run only in the `clio index` command and
once at the end of the MCP leader's startup catch-up; the catch-ups that run before
reads (MCP per call, CLI commands), the watcher, and `install-mcp` SHALL NOT sweep. Activity facts (`tool_calls`,
`tool_targets`) SHALL come from the parsed events, so a row stored without raw_json
loses none, and the activity repair pass SHALL tolerate such rows.

#### Scenario: Read catch-ups do not sweep
- **WHEN** retention is set and an aged session written with raw_json is unchanged on
  disk, and the MCP per-call catch-up and the CLI read catch-up run
- **THEN** its raw_json is kept, and a later `clio index` blanks it

#### Scenario: Codex rollouts follow the window
- **WHEN** retention is `14d` and a Codex rollout older than 14 days is ingested
- **THEN** its rows store `raw_json = ''`

#### Scenario: Repair pass over blanked rows
- **WHEN** a session was ingested with its raw_json dropped and `RepairActivity` runs
- **THEN** it succeeds and the session's tool targets are unchanged

### Requirement: Migration 0018 backfills ended_at the old incremental upsert cleared
Migration `0018_backfill_session_ended_at.sql` SHALL set `sessions.ended_at` to the
session's largest `messages.ts` (both unix seconds from the same line timestamp) for
every session whose `ended_at` is NULL and that has at least one timestamped message,
and SHALL change no other row. It repairs rows written before the fix to incremental
`upsertSession`, where a batch without a timestamp stored `ended_at = NULL` for a
timestamped session (SQLite's multi-argument `MAX` returns NULL when any argument is
NULL), so retention aged it by `started_at`. For Codex, and for any session whose newest timestamped line
produced no message (e.g. an `event_msg`), the backfilled value is a lower bound of the
original `ended_at`. Decided 2026-11-07.

#### Scenario: NULL ended_at with timestamped messages is backfilled
- **WHEN** a session has `ended_at` NULL and messages with timestamps, and migration
  0018 runs
- **THEN** its `ended_at` equals the largest message `ts`, for Claude Code, Codex, and
  rows without a source alike

#### Scenario: Other sessions are left alone
- **WHEN** migration 0018 runs over a session whose `ended_at` is already set (even if a
  message is newer), a session whose messages carry no timestamp, and a session with no
  messages
- **THEN** each keeps its stored `ended_at` (the last two stay NULL)

#### Scenario: Re-running the migration changes nothing
- **WHEN** migration 0018 is applied a second time to the same database
- **THEN** every session's `ended_at` is identical to the result of the first run

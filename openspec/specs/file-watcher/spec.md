# file-watcher Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.

## Requirements

### Requirement: Live incremental ingest in MCP mode

The system SHALL watch `~/.claude/projects/` for changes while the MCP server runs and incrementally ingest them.

#### Scenario: Existing file appended

- **WHEN** a watched `.jsonl` file receives new lines
- **THEN** the system SHALL, after a debounce window, run incremental ingest on that file

#### Scenario: New project directory created

- **WHEN** a new project directory appears under `~/.claude/projects/`
- **THEN** the system SHALL add a watch for it and ingest its files

### Requirement: Backstop reconciliation

The system SHALL periodically reconcile against the filesystem to recover missed events
and to mark sources that no longer exist as retained (see session-ingest "Indexed history
outlives its source transcript"); deleting their rows is the explicit
`clio index --purge-missing`.

#### Scenario: Dropped fsnotify event

- **WHEN** 60 seconds elapse
- **THEN** the system SHALL perform a full walk comparing against `ingest_state` and
  ingest any files missed by the watcher

#### Scenario: Deleted source retained after confirmation

- **WHEN** reconciliation finds a `source_file` recorded in the database that no longer
  exists on disk (a `not-exist` stat result, not a transient or permission error)
- **THEN** the system SHALL keep that session's rows and set its
  `ingest_state.source_missing_at`, so the conversation stays searchable after Claude Code
  deletes the transcript

#### Scenario: Explicit purge deletes still-missing sources

- **WHEN** `clio index --purge-missing` runs and a marked source is still missing
- **THEN** the system SHALL delete that session's rows from `sessions`, `messages` (and
  the FTS index via the delete triggers), `tool_calls`, `tool_targets`, `session_usage`,
  and `ingest_state`

#### Scenario: Reappearing file is re-ingested

- **WHEN** a source file that was purged exists again on a later pass
- **THEN** the system SHALL re-ingest it from scratch, restoring its rows

#### Scenario: Filesystem unavailable does not purge

- **WHEN** the projects root directory itself is missing or unreadable (e.g. home not
  mounted), or a `--purge-missing` pass without `--force` finds a set of sources that went
  missing together (marked within 24 hours of each other, inclusive) that is both a large absolute count
  and most of all known sources
- **THEN** the system SHALL skip purging entirely for that pass and leave all rows intact,
  treating a mass disappearance as an environment problem rather than deletions; a small
  number of genuine deletions (even all sources on a tiny install) SHALL still purge

### Requirement: Single-writer ownership

The system SHALL act as the sole writer while the MCP server runs.

At startup the leader SHALL run exactly one catch-up (ingest, activity backfill and deletion
reconciliation) and SHALL NOT run a separate synchronous ingest before the MCP server starts
serving; the server SHALL accept requests immediately. The leader SHALL begin renewing its
lease before that catch-up starts and keep renewing for as long as it is leader, so a catch-up
longer than the lease TTL does not forfeit leadership. If renewal reports that another process
holds the lease, any catch-up in progress SHALL be cancelled and the process SHALL demote to
follower. The process SHALL be treated as leader for read purposes only once the catch-up has
completed (a process SHALL start with that state unset, even when it holds the lease); reads arriving earlier SHALL use the follower read catch-up, which SHALL NOT run
concurrently with the leader's catch-up, nor start a rebuild of its own before the leader's catch-up has begun. The watcher's periodic reconciliation walk SHALL NOT run concurrently with such a walk on the same ingester, and on shutdown or demotion the catch-up, watcher and renewal SHALL have stopped before the lease is released.

#### Scenario: Lock file written on start

- **WHEN** `clio mcp` starts
- **THEN** the system SHALL write a lock file containing its pid so CLI invocations defer to read-only

#### Scenario: Lease renewed during a long startup catch-up

- **WHEN** the leader's startup catch-up runs longer than the lease TTL
- **THEN** the lease heartbeat SHALL keep being refreshed during the catch-up

#### Scenario: Superseded during startup catch-up

- **WHEN** lease renewal finds another process owns the lease while the catch-up runs
- **THEN** the catch-up SHALL be cancelled and the process SHALL demote to follower

#### Scenario: Early read during startup catch-up

- **WHEN** an MCP read arrives while the leader's catch-up is still running
- **THEN** it SHALL be served without waiting for the catch-up and without starting a second concurrent ingest

#### Scenario: Cancelled catch-up does not continue

- **WHEN** the startup catch-up is cancelled (superseded or shutdown) between phases
- **THEN** later phases (activity backfill, deletion reconciliation) SHALL NOT run and the process SHALL NOT be marked leader

### Requirement: Ingest spans multiple source roots

In MCP mode the file watcher SHALL watch every registered source root and live-ingest new
activity from each. Deletion reconciliation SHALL be partitioned per root: a source root
that is missing or unavailable SHALL NOT cause its previously indexed rows to be purged,
and SHALL NOT be treated as authorization to purge another source's rows.

#### Scenario: A new Codex session is live-ingested

- **WHEN** the watcher is running and a new `~/.codex/sessions/**/rollout-*.jsonl` appears
- **THEN** it SHALL be ingested as a `codex` session without a manual command

#### Scenario: A missing Codex root does not purge its rows

- **WHEN** the Codex root is temporarily unavailable while the Claude Code root is healthy
- **THEN** indexed Codex rows SHALL be preserved, not purged

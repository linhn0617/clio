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
and to purge sources that no longer exist.

#### Scenario: Dropped fsnotify event

- **WHEN** 60 seconds elapse
- **THEN** the system SHALL perform a full walk comparing against `ingest_state` and
  ingest any files missed by the watcher

#### Scenario: Deleted source purged after confirmation

- **WHEN** reconciliation finds a `source_file` recorded in the database that no longer
  exists on disk (a `not-exist` stat result, not a transient or permission error)
- **THEN** the system SHALL delete that session's rows from `sessions`, `messages` (and
  the FTS index via the delete triggers), `tool_calls`, and `ingest_state`, so a deleted
  conversation stops appearing in search

#### Scenario: Reappearing file is re-ingested

- **WHEN** a source file that was purged exists again on a later pass
- **THEN** the system SHALL re-ingest it from scratch, restoring its rows

#### Scenario: Filesystem unavailable does not purge

- **WHEN** the projects root directory itself is missing or unreadable (e.g. home not
  mounted), or a single reconciliation pass would purge a set that is both a large absolute
  count and most of all known sources
- **THEN** the system SHALL skip purging entirely for that pass and leave all rows intact,
  treating a mass disappearance as an environment problem rather than deletions; a small
  number of genuine deletions (even all sources on a tiny install) SHALL still purge

### Requirement: Single-writer ownership

The system SHALL act as the sole writer while the MCP server runs.

#### Scenario: Lock file written on start

- **WHEN** `clio mcp` starts
- **THEN** the system SHALL write a lock file containing its pid so CLI invocations defer to read-only


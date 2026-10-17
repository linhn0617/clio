## MODIFIED Requirements

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

## MODIFIED Requirements

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

# diagnostics Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.
## Requirements
### Requirement: Health diagnostics

The system SHALL provide `clio doctor` to check paths, DB integrity, ingest lag, orphaned
sessions, FTS health, and file permissions on its private files. The command SHALL exit
with a non-zero status when any check fails. A query or row-scan error while running a
check SHALL mark that check failed rather than be silently treated as a passing or
zero-count result.

#### Scenario: Healthy system

- **WHEN** the user runs `clio doctor` on a healthy install
- **THEN** the system SHALL report all checks passing, including `PRAGMA integrity_check`,
  and exit with status 0

#### Scenario: Failed check exits non-zero

- **WHEN** any `clio doctor` check reports a warning/failure
- **THEN** the command SHALL print the failing check and exit with a non-zero status

#### Scenario: Query error marks the check failed

- **WHEN** a check's underlying query or row scan returns an error
- **THEN** the system SHALL mark that check as failed (surfacing the error detail) instead
  of reporting it as passing

#### Scenario: World-readable private file flagged

- **WHEN** the database file or its `-wal`/`-shm` sidecars (which hold indexed content)
  have a mode other than `0600`
- **THEN** `clio doctor` SHALL report a permissions warning naming the file and its mode

### Requirement: Source-of-truth reconciliation

The system SHALL detect semantic drift between the DB and the source `.jsonl` files,
including content that was read but could not be indexed.

#### Scenario: Truncated source file

- **WHEN** a `.jsonl` file is truncated or missing a tail relative to what was ingested
- **THEN** `clio doctor` SHALL flag the affected session as out of sync

#### Scenario: Unparseable source lines reported

- **WHEN** ingest has skipped one or more complete lines that could not be parsed
  (recorded in `ingest_state.unparsed_lines`)
- **THEN** `clio doctor` SHALL report the total count as a warning and suggest running
  `clio index --full` after upgrading clio; a total of zero SHALL NOT warn

### Requirement: Storage warning

The system SHALL warn when the database grows large relative to source data.

#### Scenario: Oversized index

- **WHEN** the DB size exceeds the expected multiple of source `.jsonl` size
- **THEN** `clio doctor` SHALL emit a size warning and suggest `--exclude-tool-output` rebuild

### Requirement: Diagnostics are source-partitioned

`clio doctor` SHALL report ingest health per source, distinguishing a source that is
healthy and reconciled from one whose rows are preserved because its root is currently
unavailable (purge skipped). It SHALL report any cross-source uuid conflicts recorded in
`source_conflicts`, and SHALL report files found under a registered source root that no
adapter owns.

#### Scenario: A missing source root is reported as preserved, not healthy

- **WHEN** a source's root is unavailable and its rows are retained
- **THEN** `doctor` SHALL report that source's rows as preserved (root unavailable), not as fully reconciled

#### Scenario: Source conflicts are surfaced

- **WHEN** a `source_conflicts` row exists
- **THEN** `doctor` SHALL report the unindexed file and the conflicting sources


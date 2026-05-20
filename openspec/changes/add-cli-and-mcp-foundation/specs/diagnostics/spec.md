## ADDED Requirements

### Requirement: Health diagnostics

The system SHALL provide `clio doctor` to check paths, DB integrity, ingest lag, orphaned sessions, and FTS health.

#### Scenario: Healthy system

- **WHEN** the user runs `clio doctor` on a healthy install
- **THEN** the system SHALL report all checks passing, including `PRAGMA integrity_check`

### Requirement: Source-of-truth reconciliation

The system SHALL detect semantic drift between the DB and the source `.jsonl` files.

#### Scenario: Truncated source file

- **WHEN** a `.jsonl` file is truncated or missing a tail relative to what was ingested
- **THEN** `clio doctor` SHALL flag the affected session as out of sync

### Requirement: Storage warning

The system SHALL warn when the database grows large relative to source data.

#### Scenario: Oversized index

- **WHEN** the DB size exceeds the expected multiple of source `.jsonl` size
- **THEN** `clio doctor` SHALL emit a size warning and suggest `--exclude-tool-output` rebuild

## MODIFIED Requirements

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

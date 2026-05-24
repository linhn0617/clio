## MODIFIED Requirements

### Requirement: Per-message size cap and atomic per-file ingest

The system SHALL cap FTS content per message and commit each file in a single transaction.
It SHALL also bound the number of bytes read from a source file in a single pass, ingesting
an oversized file in line-aligned chunks across passes rather than reading it entirely into
memory.

#### Scenario: Oversized tool output

- **WHEN** a single message's content exceeds 64KB
- **THEN** the system SHALL truncate the FTS-indexed content while preserving the full `raw_json`

#### Scenario: Crash mid-file

- **WHEN** ingest of a file is interrupted before commit
- **THEN** the system SHALL leave `ingest_state` unchanged so the next run re-ingests that file from its prior offset

#### Scenario: Oversized file is read in bounded chunks

- **WHEN** a source file has more unread bytes than the per-pass read cap
- **THEN** the system SHALL ingest the complete lines that fit within the cap, advance its
  offset, and ingest the remainder on subsequent passes (a single line larger than the cap
  is skipped with a logged warning rather than read fully into memory)

## MODIFIED Requirements

### Requirement: Incremental append-aware ingest

The system SHALL re-ingest only newly appended content using a last-complete-newline
offset plus head and tail fingerprint checks, SHALL stream the unread tail with bounded
memory, and SHALL never silently drop a complete line it cannot parse.

#### Scenario: File grew by a partial line

- **WHEN** a `.jsonl` file has grown since last ingest and its tail ends mid-line
- **THEN** the system SHALL parse only up to the last complete newline, persist that
  offset as `last_byte_offset`, and leave the partial trailing bytes for the next run

#### Scenario: Same-size rewrite detected

- **WHEN** a file's size and mtime suggest no change but its tail fingerprint differs
  from the stored fingerprint
- **THEN** the system SHALL treat the file as changed and re-ingest

#### Scenario: Pre-watermark rewrite detected by head fingerprint

- **WHEN** a file is rewritten so that its leading bytes differ from the stored head
  fingerprint, even if the tail bytes at the stored offset are unchanged
- **THEN** the system SHALL fall back to a full reingest rather than resuming an append

#### Scenario: Large unread tail is read with bounded memory

- **WHEN** the unread tail between the watermark and EOF is large
- **THEN** the system SHALL stream complete lines without loading the entire tail into
  memory at once, while preserving single-transaction-per-file commit

#### Scenario: Complete line that cannot be parsed

- **WHEN** ingest reads a complete (newline-terminated) line that fails to parse
- **THEN** the system SHALL skip that line, increment a persistent per-source
  `unparsed_lines` counter, and still advance the watermark so the failure does not
  block later lines in the same file

### Requirement: Per-message size cap and atomic per-file ingest

The system SHALL cap FTS content per message and commit each file in a single
transaction, and SHALL abort a commit whose source snapshot can no longer be validated.

#### Scenario: Oversized tool output

- **WHEN** a single message's content exceeds 64KB
- **THEN** the system SHALL truncate the FTS-indexed content while preserving the full
  `raw_json`

#### Scenario: Crash mid-file

- **WHEN** ingest of a file is interrupted before commit
- **THEN** the system SHALL leave `ingest_state` unchanged so the next run re-ingests
  that file from its prior offset

#### Scenario: Source changed or unreadable during commit re-validation

- **WHEN** the source file's size or mtime changed since it was read, OR the file can no
  longer be stat'd (removed or replaced) at commit time
- **THEN** the system SHALL abort the commit without writing, leave `ingest_state`
  unchanged, and let a later pass re-ingest the fresh bytes

## ADDED Requirements

### Requirement: Full reindex optimizes the FTS index

The system SHALL run the FTS5 `optimize` command on `messages_fts` after a successful
`clio index --full` (including any `--purge-missing` deletion in the same run), printing
`optimizing fts index…` before and `fts optimized` after. A failed optimize SHALL be logged as
a warning and SHALL NOT fail the command, since the index content is already correct.
Incremental `clio index` SHALL NOT run it.

#### Scenario: Full reindex merges FTS segments

- **WHEN** `clio index --full` completes after earlier full reindexes left unmerged segments
- **THEN** the output SHALL include `fts optimized` and `messages_fts_data` SHALL hold fewer
  rows than before

#### Scenario: Incremental index does not optimize

- **WHEN** `clio index` runs without `--full`
- **THEN** no optimize SHALL run and no `fts` line SHALL be printed

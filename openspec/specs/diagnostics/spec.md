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

`clio doctor` SHALL report ingest health per source **by iterating the registered sources
from the source registry**, not by special-casing any source by name, distinguishing a
source that is healthy and reconciled from one whose rows are preserved because its root is
currently unavailable (purge skipped). A source whose root is optional and absent (for
example a tool that is not installed) SHALL be reported as not-installed, never as a
failure. It SHALL report any cross-source uuid conflicts recorded in `source_conflicts`,
and SHALL report files found under a registered source root that no adapter owns.

#### Scenario: A missing source root is reported as preserved, not healthy

- **WHEN** a source's root is unavailable and its rows are retained
- **THEN** `doctor` SHALL report that source's rows as preserved (root unavailable), not as fully reconciled

#### Scenario: Source conflicts are surfaced

- **WHEN** a `source_conflicts` row exists
- **THEN** `doctor` SHALL report the unindexed file and the conflicting sources

#### Scenario: Per-source health is driven by the registry

- **WHEN** `doctor` runs with `claude-code` and `codex` registered
- **THEN** it SHALL report each source's health by iterating the registry, producing the
  same per-source output as before without a codex-specific branch

### Requirement: doctor reports pruned raw_json
`clio doctor` SHALL report two well-defined figures (original raw_json lengths
are gone after pruning, so no per-raw byte estimate is fabricated): the exact
pruned-message count (`COUNT(*) WHERE raw_json=''`), and the whole-DB freelist
size (`freelist_count × page_size`) labeled as "reclaimable on VACUUM (whole-DB
freelist, not raw-only)". Informational, not a failing check.

#### Scenario: doctor surfaces pruned counts
- **WHEN** `clio doctor` runs on a database where some sessions have been pruned
- **THEN** the output includes the exact pruned-message count and the labeled
  whole-DB freelist figure

#### Scenario: doctor on an unpruned database
- **WHEN** `clio doctor` runs on a database with no pruned messages
- **THEN** the pruned-raw line reports zero (or is a clean pass), not an error

### Requirement: doctor reports retained sessions

`clio doctor` SHALL print a `retained sessions` line, always (including a count of 0), of the
form `N session(s) kept after their transcript was deleted (clio index --purge-missing to
drop)`, where N counts sessions whose `ingest_state.source_missing_at` is set. It is
informational, never a failing check. Source reconciliation SHALL count a vanished file that
is marked as retained rather than missing (missing is only a vanished, unmarked file under an
available root), and ingest coverage SHALL exclude marked rows from the tracked count, so
long retention neither warns nor masks a real coverage gap. On a database that predates the
`source_missing_at` column (doctor opens read-only, before migrations), nothing SHALL be
treated as retained and no check SHALL fail for the missing column.

#### Scenario: Retained session is reported and not a failure

- **WHEN** a session's transcript was deleted and its row is marked retained
- **THEN** `clio doctor` SHALL report `1 session(s) kept after their transcript was deleted
  (clio index --purge-missing to drop)` and source reconciliation SHALL pass

#### Scenario: Zero retained is still printed

- **WHEN** no session is retained
- **THEN** the `retained sessions` line SHALL report 0 as a passing check

#### Scenario: Unmarked vanished file is still missing

- **WHEN** a tracked file under an available root is gone but not yet marked
- **THEN** source reconciliation SHALL count it as missing and warn

#### Scenario: Coverage excludes retained rows

- **WHEN** a root has 2 files on disk, 1 tracked, and 1 retained row
- **THEN** ingest coverage SHALL report `2 on disk, 1 tracked` and warn

### Requirement: doctor reports FTS bloat

`clio doctor` SHALL print an `fts bloat` line of the form
`fts N MB vs messages M MB (ratio R)`, sizes taken from the `dbstat` virtual table for
`messages_fts_data` and `messages`. The check SHALL fail, appending
`run clio index --full to optimize`, when the ratio exceeds 2.0 and the FTS data is at least
16 MB (smaller databases are never flagged). If the measurement itself fails the line SHALL
read `unavailable (…)` and SHALL pass: an absent measuring tool is not an index defect.

#### Scenario: Bloated FTS index is flagged

- **WHEN** `messages_fts_data` is 2135 MB and `messages` is 557 MB
- **THEN** the line SHALL read `fts 2135.0 MB vs messages 557.0 MB (ratio 3.8); run clio
  index --full to optimize` and the check SHALL fail

#### Scenario: Healthy or small index passes

- **WHEN** the ratio is at most 2.0, or the FTS data is under 16 MB
- **THEN** the check SHALL pass

#### Scenario: Measurement failure is not red

- **WHEN** the `dbstat` query errors
- **THEN** the line SHALL say `unavailable` and the check SHALL pass


## ADDED Requirements

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

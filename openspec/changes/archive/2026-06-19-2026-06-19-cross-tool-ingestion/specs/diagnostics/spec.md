## ADDED Requirements

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

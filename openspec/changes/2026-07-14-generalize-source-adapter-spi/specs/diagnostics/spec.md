## MODIFIED Requirements

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

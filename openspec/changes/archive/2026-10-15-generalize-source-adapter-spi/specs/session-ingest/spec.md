## ADDED Requirements

### Requirement: The set of source names comes from a source registry

The system SHALL maintain a source registry — a compile-time static-seeded list of the
registered sources' names, display labels, and root availability — that is the single
source of truth for the set of source names. Surfaces that enumerate or branch on sources —
CLI `--source` validation and help, the CLI bootstrap source-availability check, the MCP
read tools' `source` enum and defaults, the `doctor` per-source report, the TUI source
labels, and the database source-filter default — SHALL derive their values from this
registry rather than from hardcoded literals or per-source-name branches. With only
`claude-code` and `codex` seeded, every derived value SHALL be identical to the previously
hardcoded `{claude-code, codex, all}` values and the `[codex]` label, so behavior is
unchanged. Ingest semantics, incremental behavior, the `Source` interface, and the schema
SHALL be unchanged by this requirement.

#### Scenario: A source added to the seed appears without editing each surface

- **WHEN** a new source entry is added to the registry seed
- **THEN** its name SHALL become a valid `--source` and MCP `source` value and SHALL be
  labeled in the TUI and reported by `doctor`, without editing the CLI, MCP, TUI, or
  doctor source lists individually

#### Scenario: The default and enum are unchanged for the current two sources

- **WHEN** only `claude-code` and `codex` are seeded
- **THEN** the derived `--source`/MCP enum SHALL be exactly `claude-code`, `codex`, `all`
  with default `claude-code`, and the TUI SHALL label codex rows `[codex]` as before

#### Scenario: The registry is static within a process

- **WHEN** the MCP server has advertised its tools' `source` enum
- **THEN** the set of accepted source names SHALL NOT change for the life of the process,
  because the registry is seeded at compile time rather than registered at runtime

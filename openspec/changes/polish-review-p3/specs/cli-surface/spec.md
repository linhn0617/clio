## MODIFIED Requirements

### Requirement: Search and show output formats

The system SHALL support human-readable and JSON output. `show` SHALL accept `--json` as an
alias for `--format json` and a `--limit` flag bounding the number of messages rendered. In
`show --format raw`, the system SHALL collapse consecutive messages that share an identical
`raw_json` into a single printed line (the multiple content blocks expanded from one source
event share one `raw_json`), so the raw dump does not repeat that event's line. Resolving a
session id SHALL treat an exact uuid match as authoritative even when it is also a prefix of
other session ids.

#### Scenario: JSON output for piping

- **WHEN** the user runs `clio search <query> --json`
- **THEN** the system SHALL emit machine-readable JSON suitable for piping

#### Scenario: Show with the --json convenience flag

- **WHEN** the user runs `clio show <id> --json`
- **THEN** the system SHALL render the session as JSON, equivalent to `--format json`

#### Scenario: Show full session

- **WHEN** the user runs `clio show <uuid-prefix>` with an unambiguous prefix
- **THEN** the system SHALL render the full session in the requested format (markdown by
  default), resolving the prefix to a single session

#### Scenario: Show message limit

- **WHEN** the user runs `clio show <id> --limit N`
- **THEN** the system SHALL render at most N messages, rather than a hard-coded ceiling

#### Scenario: Raw format collapses an event's repeated line

- **WHEN** the user runs `clio show <id> --format raw` for a session whose source line
  expanded into several adjacent stored messages sharing the same `raw_json`
- **THEN** the system SHALL print that `raw_json` once for the run of identical adjacent
  lines, while adjacent messages with differing `raw_json` each print

#### Scenario: Exact id wins over prefix collisions

- **WHEN** a session uuid is an exact match for the argument and is also a prefix of other
  session uuids
- **THEN** the system SHALL resolve to the exact session rather than reporting an ambiguous
  prefix

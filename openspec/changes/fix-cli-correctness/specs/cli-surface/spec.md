## MODIFIED Requirements

### Requirement: Search and show output formats

The system SHALL support human-readable and JSON output. The `show --format raw` output
SHALL reproduce the source `.jsonl` events without repeating an event line that expanded
into multiple stored messages. Resolving a session id SHALL treat an exact uuid match as
authoritative even when it is also a prefix of other session ids.

#### Scenario: JSON output for piping

- **WHEN** the user runs `clio search <query> --json`
- **THEN** the system SHALL emit machine-readable JSON suitable for piping

#### Scenario: Show full session

- **WHEN** the user runs `clio show <uuid-prefix>` with an unambiguous prefix
- **THEN** the system SHALL render the full session in the requested format (markdown by
  default), resolving the prefix to a single session

#### Scenario: Raw format does not repeat event lines

- **WHEN** the user runs `clio show <id> --format raw` for a session whose source line
  expanded into several stored messages sharing the same `raw_json`
- **THEN** the system SHALL print each distinct source event line once, not once per
  expanded message

#### Scenario: Exact id wins over prefix collisions

- **WHEN** a session uuid is an exact match for the argument and is also a prefix of other
  session uuids
- **THEN** the system SHALL resolve to the exact session rather than reporting an ambiguous
  prefix

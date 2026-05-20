# cli-surface Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.
## Requirements
### Requirement: CLI subcommands

The system SHALL expose `index`, `search`, `list`, `show`, `mcp`, `doctor`, `install-mcp`, and `uninstall-mcp` subcommands via a single `clio` binary.

#### Scenario: Help lists subcommands

- **WHEN** the user runs `clio --help`
- **THEN** the system SHALL list all subcommands with short descriptions

### Requirement: Search and show output formats

The system SHALL support human-readable and JSON output.

#### Scenario: JSON output for piping

- **WHEN** the user runs `clio search <query> --json`
- **THEN** the system SHALL emit machine-readable JSON suitable for piping

#### Scenario: Show full session

- **WHEN** the user runs `clio show <uuid-prefix>` with an unambiguous prefix
- **THEN** the system SHALL render the full session in the requested format (markdown by default), resolving the prefix to a single session

### Requirement: CLI defers to MCP writer

The system SHALL skip its own incremental ingest when an MCP server is the active writer.

#### Scenario: MCP server running

- **WHEN** a valid MCP lock file exists and the user runs a CLI query
- **THEN** the CLI SHALL query read-only without performing incremental ingest


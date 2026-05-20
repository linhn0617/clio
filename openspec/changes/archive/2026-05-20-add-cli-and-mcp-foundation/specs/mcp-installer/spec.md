## ADDED Requirements

### Requirement: Two-phase MCP installation

The system SHALL install the MCP server in two phases: a full ingest first, then a config write only on ingest success.

#### Scenario: Ingest fails

- **WHEN** the user runs `clio install-mcp` and the initial full ingest fails
- **THEN** the system SHALL report the error and leave `~/.claude.json` untouched

#### Scenario: Successful install

- **WHEN** the initial full ingest succeeds
- **THEN** the system SHALL atomically write the `clio` server entry into `~/.claude.json`

### Requirement: Safe config mutation

The system SHALL back up and atomically write `~/.claude.json`, preserving existing entries.

#### Scenario: Existing servers preserved

- **WHEN** `~/.claude.json` already contains other MCP servers
- **THEN** the system SHALL create a `.bak` backup, merge the `clio` entry without overwriting others, write to a temp file, fsync, atomically rename, and remove the backup only after verifying the result parses

#### Scenario: Idempotent re-install

- **WHEN** a `clio` entry already exists
- **THEN** the system SHALL update it in place without duplicating

### Requirement: Uninstall

The system SHALL remove the `clio` entry safely.

#### Scenario: Uninstall removes only clio

- **WHEN** the user runs `clio uninstall-mcp`
- **THEN** the system SHALL remove only the `clio` server entry, preserving all others, using the same atomic + backup procedure

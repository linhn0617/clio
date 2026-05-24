# mcp-installer Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.
## Requirements
### Requirement: Two-phase MCP installation

The system SHALL install the MCP server in two phases: a full ingest first, then a config write only on ingest success.

#### Scenario: Ingest fails

- **WHEN** the user runs `clio install-mcp` and the initial full ingest fails
- **THEN** the system SHALL report the error and leave `~/.claude.json` untouched

#### Scenario: Successful install

- **WHEN** the initial full ingest succeeds
- **THEN** the system SHALL atomically write the `clio` server entry into `~/.claude.json`

### Requirement: Safe config mutation

The system SHALL back up and atomically write `~/.claude.json`, preserving existing
entries. When `mcpServers` is present with a non-null value that is not a JSON object, the
system SHALL return an error and leave the config file untouched rather than overwriting
the unexpected value. A missing key or a JSON `null` value carries no server data and SHALL
be treated as absent (a fresh object is created). The system SHALL NOT leave a `.bak` file
behind on any exit path: the backup is removed after a successful write and also when a
write step fails after the backup was created (the atomic rename keeps the original intact).

#### Scenario: Existing servers preserved

- **WHEN** `~/.claude.json` already contains other MCP servers
- **THEN** the system SHALL create a `.bak` backup, merge the `clio` entry without
  overwriting others, write to a temp file, fsync, atomically rename, and remove the backup
  only after verifying the result parses

#### Scenario: Idempotent re-install

- **WHEN** a `clio` entry already exists
- **THEN** the system SHALL update it in place without duplicating

#### Scenario: Non-object mcpServers is rejected

- **WHEN** `~/.claude.json` has an `mcpServers` value that is non-null and not a JSON object
  (e.g. an array or string)
- **THEN** the system SHALL return an error and leave the file byte-for-byte unchanged,
  without writing a backup or temp file

#### Scenario: Null mcpServers is treated as absent

- **WHEN** `~/.claude.json` has `"mcpServers": null`
- **THEN** the system SHALL create a fresh `mcpServers` object and write the entry, the
  same as for a missing key

#### Scenario: No backup left behind on a failed write

- **WHEN** a write step fails after the `.bak` backup has been created
- **THEN** the system SHALL remove the `.bak` (leaving no backup behind), the original
  config remaining intact via the atomic rename

### Requirement: Uninstall

The system SHALL remove the `clio` entry safely.

#### Scenario: Uninstall removes only clio

- **WHEN** the user runs `clio uninstall-mcp`
- **THEN** the system SHALL remove only the `clio` server entry, preserving all others, using the same atomic + backup procedure


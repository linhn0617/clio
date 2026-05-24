## MODIFIED Requirements

### Requirement: Safe config mutation

The system SHALL back up and atomically write `~/.claude.json`, preserving existing
entries. When `mcpServers` is present but is not a JSON object, the system SHALL return an
error and leave the config file untouched rather than overwriting the unexpected value.

#### Scenario: Existing servers preserved

- **WHEN** `~/.claude.json` already contains other MCP servers
- **THEN** the system SHALL create a `.bak` backup, merge the `clio` entry without
  overwriting others, write to a temp file, fsync, atomically rename, and remove the backup
  only after verifying the result parses

#### Scenario: Idempotent re-install

- **WHEN** a `clio` entry already exists
- **THEN** the system SHALL update it in place without duplicating

#### Scenario: Non-object mcpServers is rejected

- **WHEN** `~/.claude.json` has an `mcpServers` value that is not a JSON object (e.g. an
  array or string)
- **THEN** the system SHALL return an error and leave the file byte-for-byte unchanged,
  without writing a backup or temp file

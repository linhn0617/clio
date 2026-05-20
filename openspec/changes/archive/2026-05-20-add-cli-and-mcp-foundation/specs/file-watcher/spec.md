## ADDED Requirements

### Requirement: Live incremental ingest in MCP mode

The system SHALL watch `~/.claude/projects/` for changes while the MCP server runs and incrementally ingest them.

#### Scenario: Existing file appended

- **WHEN** a watched `.jsonl` file receives new lines
- **THEN** the system SHALL, after a debounce window, run incremental ingest on that file

#### Scenario: New project directory created

- **WHEN** a new project directory appears under `~/.claude/projects/`
- **THEN** the system SHALL add a watch for it and ingest its files

### Requirement: Backstop reconciliation

The system SHALL periodically reconcile against the filesystem to recover missed events.

#### Scenario: Dropped fsnotify event

- **WHEN** 60 seconds elapse
- **THEN** the system SHALL perform a full walk comparing against `ingest_state` and ingest any files missed by the watcher

### Requirement: Single-writer ownership

The system SHALL act as the sole writer while the MCP server runs.

#### Scenario: Lock file written on start

- **WHEN** `clio mcp` starts
- **THEN** the system SHALL write a lock file containing its pid so CLI invocations defer to read-only

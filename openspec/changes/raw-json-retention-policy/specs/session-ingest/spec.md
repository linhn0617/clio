## ADDED Requirements

### Requirement: Every ingest path applies the raw retention window
The write-time raw_json drop of the raw-json-retention capability SHALL apply to every
ingest path — `clio index`, `clio index --full`, the CLI read catch-up, the MCP
leader's startup catch-up and read catch-up, the watcher, and `install-mcp` — and to
every source (Claude Code, Codex). The setting SHALL be read once per `IngestAll` or
`IngestFile` call. The retention sweep SHALL run only in the `clio index` command and
once at the end of the MCP leader's startup catch-up; the catch-ups that run before
reads (MCP per call, CLI commands), the watcher, and `install-mcp` SHALL NOT sweep. Activity facts (`tool_calls`,
`tool_targets`) SHALL come from the parsed events, so a row stored without raw_json
loses none, and the activity repair pass SHALL tolerate such rows.

#### Scenario: Read catch-ups do not sweep
- **WHEN** retention is set and an aged session written with raw_json is unchanged on
  disk, and the MCP per-call catch-up and the CLI read catch-up run
- **THEN** its raw_json is kept, and a later `clio index` blanks it

#### Scenario: Codex rollouts follow the window
- **WHEN** retention is `14d` and a Codex rollout older than 14 days is ingested
- **THEN** its rows store `raw_json = ''`

#### Scenario: Repair pass over blanked rows
- **WHEN** a session was ingested with its raw_json dropped and `RepairActivity` runs
- **THEN** it succeeds and the session's tool targets are unchanged

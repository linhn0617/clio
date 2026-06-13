## ADDED Requirements

### Requirement: Structured activity index from tool calls

At ingest the system SHALL extract structured activity facts from each non-clio
tool_use and store them in a `tool_targets` table keyed by message and session.
It SHALL record a `tool` fact (the tool name, including MCP `mcp__server__tool`)
for every tool_use, plus a domain fact where applicable: `file`
(Edit/Write/Read/NotebookEdit/MultiEdit file path), `command` (Bash), `pattern`
(Grep/Glob), or `url` (WebFetch). Stored values SHALL be redacted, and clio's own
MCP tools (`mcp__clio__*`) SHALL be excluded.

#### Scenario: Edit records a tool fact and a file fact

- **WHEN** a session contains an `Edit` tool_use on `/x/auth.ts`
- **THEN** `tool_targets` SHALL contain a `tool` fact (`Edit`) and a `file` fact
  (`/x/auth.ts`) for that message

#### Scenario: Bash records a command fact

- **WHEN** a session contains a `Bash` tool_use running `go test ./...`
- **THEN** `tool_targets` SHALL contain a `command` fact whose value is the
  redacted command

#### Scenario: clio's own tools are excluded

- **WHEN** a tool_use is `mcp__clio__search`
- **THEN** no `tool_targets` row SHALL be created for it

### Requirement: One-time activity backfill

The system SHALL populate `tool_targets` for already-indexed messages from their
stored (redacted) `raw_json`, without re-reading source files. The backfill SHALL
be idempotent and run automatically when the table is empty.

#### Scenario: Existing history is backfilled

- **WHEN** the activity index is added to a database that already contains
  tool_use messages
- **THEN** activity queries SHALL return facts for those historical messages
  without requiring a full re-ingest

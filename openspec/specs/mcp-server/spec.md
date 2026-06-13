# mcp-server Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.
## Requirements
### Requirement: stdio MCP server with four tools

The system SHALL run a stdio MCP server exposing `search`, `list_sessions`,
`activity_summary`, and `read_session` tools. When `activity_summary` groups by day, it
SHALL bucket by the local calendar day. The system SHALL validate tool inputs at the
request boundary and return a clear tool error for an unsupported value rather than a
generic downstream failure.

#### Scenario: Search tool call

- **WHEN** an MCP client calls `search` with a query
- **THEN** the system SHALL return ranked results with snippets, respecting the requested
  limit

#### Scenario: Read session with pagination

- **WHEN** an MCP client calls `read_session` for a long session
- **THEN** the system SHALL return a page of messages (default excluding tool output) and
  a `has_more` flag

#### Scenario: Activity summary groups by local day

- **WHEN** an MCP client calls `activity_summary` with `group_by: "day"`
- **THEN** the system SHALL bucket each session by its local calendar day (not the UTC
  day)

#### Scenario: Unsupported group_by is rejected at the boundary

- **WHEN** an MCP client calls `activity_summary` with a `group_by` other than `day` or
  `project`
- **THEN** the system SHALL return a tool error naming the allowed values, without running
  the query

### Requirement: stdout purity

The system SHALL emit only JSON-RPC on stdout; all logs and errors go to stderr.

#### Scenario: Error during a tool call

- **WHEN** a tool handler encounters an error
- **THEN** the system SHALL return an MCP error response and write diagnostics to stderr, never to stdout

### Requirement: Context-overflow protection

The system SHALL clamp result sizes to protect the caller's context window.

#### Scenario: Excessive limit requested

- **WHEN** an MCP client requests a limit above the maximum
- **THEN** the system SHALL clamp the limit to 50

### Requirement: Tools advertise MCP safety annotations

The system SHALL attach MCP `ToolAnnotation` hints to every registered tool so
clients can render accurate trust labels instead of falling back to the spec's
worst-case default of destructive + open-world. Each of `search`,
`list_sessions`, `activity_summary`, and `read_session` SHALL advertise
`readOnlyHint=true`, `destructiveHint=false`, `idempotentHint=true`, and
`openWorldHint=false`.

#### Scenario: Client lists tools

- **WHEN** an MCP client calls `tools/list`
- **THEN** each of the four clio tools SHALL include an `annotations` object
  with `readOnlyHint=true`, `destructiveHint=false`, `idempotentHint=true`, and
  `openWorldHint=false`

#### Scenario: Annotations match implementation reality

- **WHEN** any clio tool handler runs
- **THEN** it SHALL only read from the local session index — never mutate state,
  delete data, or contact an external service — so the advertised
  read-only/non-destructive/closed-world hints remain truthful

### Requirement: Activity-aware MCP queries

`list_sessions` SHALL accept `touched` (path prefix), `tool` (name), and `ran`
(command substring) filters, and `activity_summary`'s `group_by` SHALL additionally
accept `file`, `command`, `tool`, `pattern`, and `url`, so Claude can answer activity
questions over the indexed history.

#### Scenario: list_sessions filtered by tool

- **WHEN** an MCP client calls `list_sessions` with `tool="Bash"`
- **THEN** only sessions that ran a `Bash` tool_use SHALL be returned

#### Scenario: list_sessions filtered by touched path

- **WHEN** an MCP client calls `list_sessions` with `touched="/x/auth"`
- **THEN** only sessions whose tool calls touched a path under `/x/auth` SHALL be
  returned

#### Scenario: activity_summary grouped by file

- **WHEN** an MCP client calls `activity_summary` with `group_by="file"`
- **THEN** the system SHALL return counts of activity grouped by file path


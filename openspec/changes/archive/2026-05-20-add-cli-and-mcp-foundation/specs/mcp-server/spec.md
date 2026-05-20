## ADDED Requirements

### Requirement: stdio MCP server with four tools

The system SHALL run a stdio MCP server exposing `search`, `list_sessions`, `activity_summary`, and `read_session` tools.

#### Scenario: Search tool call

- **WHEN** an MCP client calls `search` with a query
- **THEN** the system SHALL return ranked results with snippets, respecting the requested limit

#### Scenario: Read session with pagination

- **WHEN** an MCP client calls `read_session` for a long session
- **THEN** the system SHALL return a page of messages (default excluding tool output) and a `has_more` flag

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

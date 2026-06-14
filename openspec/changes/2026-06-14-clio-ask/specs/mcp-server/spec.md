## ADDED Requirements

### Requirement: `ask` MCP tool

The MCP server SHALL expose a read-only `ask` tool taking `question` (required) and
optional `project`, `since`, and `limit`, returning the evidence bundle as
structured JSON so the client can synthesize an answer and cite session ids. The
tool SHALL be annotated read-only and SHALL perform no generation.

#### Scenario: Ask tool returns a structured bundle

- **WHEN** the client calls `ask` with a question
- **THEN** the server SHALL return grouped, cited excerpts as JSON

#### Scenario: Ask tool is annotated read-only

- **WHEN** the tool list is inspected
- **THEN** `ask` SHALL carry a read-only annotation

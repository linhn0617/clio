## ADDED Requirements

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

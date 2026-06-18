## ADDED Requirements

### Requirement: MCP exposes subagents as parent-linked children

The MCP `list_sessions` tool SHALL exclude subagent child sessions by default and
accept an `include_subagents` parameter to include them. `read_session` on a parent
SHALL report its subagents (id, type, title), optionally inlining their messages.
`search` results SHALL carry each hit's `parent_session` and `agent_type` so a
client can attribute a hit to a subagent and its parent. All three SHALL remain
read-only.

#### Scenario: list_sessions hides subagents by default

- **WHEN** a client calls `list_sessions` with no `include_subagents`
- **THEN** subagent child sessions SHALL be omitted from the result

#### Scenario: read_session reports a parent's subagents

- **WHEN** a client calls `read_session` on a parent that spawned subagents
- **THEN** the result SHALL include the parent's subagents

#### Scenario: search hits carry parent and type

- **WHEN** a `search` hit comes from a subagent transcript
- **THEN** the result SHALL include its `parent_session` and `agent_type`

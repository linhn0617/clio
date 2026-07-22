# mcp-server (delta)

## ADDED Requirements

### Requirement: activity_summary usage dimension
The MCP `activity_summary` tool SHALL accept `group_by: "usage"` returning session-level token
aggregates (session uuid, title, project, source, per-category totals) so the MCP client can
follow up with `read_session`. Results SHALL be attributed per source and no cross-source
combined total SHALL be returned. Responses expose token aggregates only; no monetary amounts.

#### Scenario: MCP client ranks sessions by tokens
- **WHEN** an MCP client calls `activity_summary` with `group_by: "usage"` and a period
- **THEN** it receives sessions ranked by total tokens within each source, with identifiers
  accepted by `read_session`

### Requirement: Quota data never crosses MCP
The MCP server SHALL NOT include quota snapshot fields (used_percent, resets_at, window
identity, plan_type, credits, or any account-level quota data) in any tool response. There is no configuration
override in this change; quota data is CLI-only. Any future quota-over-MCP exposure requires its
own change with a real configuration design.

#### Scenario: No quota field over MCP
- **WHEN** an MCP client calls any clio tool, including `activity_summary` with
  `group_by: "usage"`
- **THEN** no quota snapshot field appears anywhere in the response

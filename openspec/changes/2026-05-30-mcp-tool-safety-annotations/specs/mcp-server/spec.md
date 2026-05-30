## ADDED Requirements

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

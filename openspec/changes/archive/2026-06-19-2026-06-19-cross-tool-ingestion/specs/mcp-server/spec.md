## ADDED Requirements

### Requirement: MCP read tools are source-filtered

The MCP read tools SHALL accept a `source` parameter (`claude-code`, `codex`, or `all`)
defaulting to `claude-code` — namely `search`, `ask`, `list_sessions`,
`activity_summary`, and `read_session` — so an MCP client sees only Claude Code history
unless it opts in. Results SHALL carry each session's source. The tools SHALL remain
read-only.

#### Scenario: list_sessions defaults to Claude Code

- **WHEN** an MCP client calls `list_sessions` with no `source` argument
- **THEN** only `claude-code` sessions SHALL be returned

#### Scenario: Codex sessions via MCP are opt-in

- **WHEN** an MCP client calls `search` with `source` `codex`
- **THEN** only `codex` results SHALL be returned, each carrying its source

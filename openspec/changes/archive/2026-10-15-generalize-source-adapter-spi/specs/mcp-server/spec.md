## MODIFIED Requirements

### Requirement: MCP read tools are source-filtered

The MCP read tools SHALL accept a `source` parameter whose enumerated values are the
registered source names plus `all`, **derived from the source registry**, defaulting to
`claude-code` — namely `search`, `ask`, `list_sessions`, `activity_summary`, and
`read_session` — so an MCP client sees only Claude Code history unless it opts in. Neither
the enum nor the default SHALL be hardcoded per tool; both SHALL come from the registry
(default = the fallback source). Results SHALL carry each session's source. The tools SHALL
remain read-only.

#### Scenario: list_sessions defaults to Claude Code

- **WHEN** an MCP client calls `list_sessions` with no `source` argument
- **THEN** only `claude-code` sessions SHALL be returned

#### Scenario: Codex sessions via MCP are opt-in

- **WHEN** an MCP client calls `search` with `source` `codex`
- **THEN** only `codex` results SHALL be returned, each carrying its source

#### Scenario: Every read tool's enum tracks the registry

- **WHEN** the registry contains only `claude-code` and `codex`
- **THEN** the `source` enum on all five read tools SHALL be exactly `claude-code`,
  `codex`, `all` with default `claude-code`, derived from the registry rather than written
  out per tool

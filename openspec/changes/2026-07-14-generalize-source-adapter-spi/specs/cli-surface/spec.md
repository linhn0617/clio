## MODIFIED Requirements

### Requirement: CLI reads are source-filtered and default to Claude Code

`clio list`, `show`, `search`, `ask`, and `activity` SHALL accept a `--source` flag whose
accepted values are the registered source names plus `all`, **derived from the source
registry**, defaulting to `claude-code` when omitted, so existing behavior is unchanged
unless another source is requested. The flag validation SHALL reject values not in the
registry (plus `all`) and SHALL NOT hardcode the source list. Each rendered session or
result SHALL show its source when sources other than Claude Code are included. `clio
recall` SHALL remain Claude-Code-only and SHALL NOT include other sources.

#### Scenario: Default listing is Claude Code only

- **WHEN** the user runs `clio list` with Codex sessions indexed
- **THEN** only `claude-code` sessions SHALL be listed

#### Scenario: Codex sessions are opted in

- **WHEN** the user runs `clio search "foo" --source codex`
- **THEN** only `codex` results SHALL be returned, each labeled with its source

#### Scenario: All sources together

- **WHEN** the user runs `clio list --source all`
- **THEN** both Claude Code and Codex sessions SHALL be listed, each showing its source

#### Scenario: recall stays Claude Code only

- **WHEN** the user runs `clio recall` with Codex sessions indexed
- **THEN** the digest SHALL cover only `claude-code` activity

#### Scenario: Accepted --source values track the registry

- **WHEN** the registry contains only `claude-code` and `codex`
- **THEN** `--source` SHALL accept exactly `claude-code`, `codex`, and `all` and reject any
  other value, without the CLI hardcoding that list

## ADDED Requirements

### Requirement: CLI bootstrap availability is registry-derived

`clio index` and `clio install-mcp` SHALL, when the Claude Code projects directory is
absent, still proceed if any registered source's root is available, determining that
availability from the source registry rather than a per-source hardcoded check. With only
`claude-code` and `codex` registered, this SHALL behave identically to the current
codex-specific check.

#### Scenario: A Codex-only machine still bootstraps

- **WHEN** `~/.claude/projects` does not exist but the Codex sessions directory does
- **THEN** `clio index` and `clio install-mcp` SHALL proceed, as today

#### Scenario: A future source-only machine bootstraps without bootstrap-code edits

- **WHEN** a new source is registered and only that source's root exists on the machine
- **THEN** `clio index` and `clio install-mcp` SHALL proceed without the bootstrap check
  being edited for that source

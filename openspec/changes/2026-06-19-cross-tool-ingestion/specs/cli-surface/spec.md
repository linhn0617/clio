## ADDED Requirements

### Requirement: CLI reads are source-filtered and default to Claude Code

`clio list`, `show`, `search`, `ask`, and `activity` SHALL accept a `--source` flag with
values `claude-code`, `codex`, or `all`, defaulting to `claude-code` when omitted, so
existing behavior is unchanged unless another source is requested. Each rendered session
or result SHALL show its source when sources other than Claude Code are included. `clio
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

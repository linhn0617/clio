## ADDED Requirements

### Requirement: Search is source-scoped

Full-text search SHALL filter by source, defaulting to `claude-code` and including other
sources only when explicitly requested. Each result SHALL carry its source.

#### Scenario: Search defaults to Claude Code

- **WHEN** a search runs without a source filter and Codex sessions are indexed
- **THEN** only `claude-code` matches SHALL be returned

#### Scenario: Codex matches are opted in

- **WHEN** a search runs with the source filter set to `codex`
- **THEN** only `codex` matches SHALL be returned, each carrying `source` `codex`

# recall-hook Specification

## Purpose
TBD - created by archiving change 2026-06-13-ambient-recall-hook. Update Purpose after archive.
## Requirements
### Requirement: Project-scoped recall digest

`clio recall` SHALL print a concise, read-only digest of recent activity for the
current project, determined from the working directory (or a `--project`
override). The digest SHALL include the project's most recent sessions (title,
date, turn count), the files it recently touched, and the commands it recently
ran. It SHALL open the index read-only (no ingest, no write-lock contention with a
running MCP server), print nothing when the project has no indexed history, and
exit 0 with empty output on any error so it can never break session startup.

#### Scenario: Digest for a project with history

- **WHEN** `clio recall` runs in a directory under an indexed project
- **THEN** it SHALL print that project's recent sessions, touched files, and
  commands

#### Scenario: No history is silent

- **WHEN** `clio recall` runs for a project with no indexed sessions
- **THEN** it SHALL print nothing and exit 0

#### Scenario: Errors never break startup

- **WHEN** the index is missing or unreadable
- **THEN** `clio recall` SHALL exit 0 with empty output

### Requirement: Opt-in SessionStart hook installation

`clio install-hook` SHALL register a Claude Code SessionStart hook (in
`~/.claude/settings.json`) that runs `clio recall`, and `clio uninstall-hook`
SHALL remove it. Installation SHALL be atomic (leaving the original config intact
on failure) and SHALL preserve any existing hooks. It is opt-in and SHALL NOT be
performed by `install-mcp`.

#### Scenario: Install preserves existing hooks

- **WHEN** a user runs `clio install-hook` with other SessionStart hooks present
- **THEN** the clio recall hook SHALL be added and the existing hooks SHALL remain

#### Scenario: Uninstall removes only clio's hook

- **WHEN** a user runs `clio uninstall-hook`
- **THEN** only the clio recall SessionStart hook SHALL be removed, leaving other
  hooks intact


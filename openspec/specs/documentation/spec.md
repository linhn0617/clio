# documentation Specification

## Purpose
TBD - created by archiving change 2026-05-30-readme-ranking-and-grep-comparison. Update Purpose after archive.
## Requirements
### Requirement: README documents search ranking and the grep comparison

The user-facing README (`README.md` and `README_zh-TW.md`) SHALL describe the
`search` tool as ranked by relevance and recency for normal queries — noting that
very short (1–2 character) queries fall back to a substring scan — and SHALL
include a section contrasting clio with grepping the raw session files
(`~/.claude/projects/*.jsonl`). The English and Traditional Chinese editions SHALL
stay in sync.

#### Scenario: Reader sees that search is ranked

- **WHEN** a user reads the MCP tools table in either README
- **THEN** the `search` entry SHALL state that results are ranked
  (relevance + recency), not merely "full-text search"

#### Scenario: Reader can weigh clio against grep

- **WHEN** a user reads the README before installing
- **THEN** a "Why not just grep?" section SHALL explain clio's advantages over
  raw grep: cross-session memory, ranked hits, excluded tool-output noise, and
  exact session resolution
- **AND** the same section SHALL be present in both the English and Traditional
  Chinese READMEs

### Requirement: Docs cover every user-facing command and MCP tool

The README (`README.md` and `README_zh-TW.md`) and `docs/USAGE.md` SHALL document
every user-facing CLI command and every MCP tool clio ships. The README MCP tools
table SHALL list every registered tool, and `docs/USAGE.md` SHALL carry a CLI
reference entry (or cheat-sheet line) for every command. The English and
Traditional Chinese READMEs SHALL stay in sync.

#### Scenario: Every MCP tool appears in the README table

- **WHEN** a user reads the MCP tools table in either README
- **THEN** it SHALL list every tool the server registers — `search`, `ask`,
  `list_sessions`, `activity_summary`, and `read_session`

#### Scenario: Every command is documented in the usage guide

- **WHEN** a user reads `docs/USAGE.md`
- **THEN** every user-facing command — including `ask`, `activity`, `recall`,
  `install-hook`, and `uninstall-hook` — SHALL have a reference entry or a
  cheat-sheet line

### Requirement: README documents transcript retention

`README.md` and `README_zh-TW.md` SHALL each explain, with the same meaning, that Claude
Code's `cleanupPeriodDays` (default 30) deletes session transcripts; that clio keeps the
indexed copy after the transcript is deleted; that raising `cleanupPeriodDays` in
`~/.claude/settings.json` keeps the original transcripts; that `clio index --purge-missing`
deletes retained sessions; and that `clio doctor` reports the retained count.

#### Scenario: Reader learns why old history survives

- **WHEN** a user reads the indexing section of either README
- **THEN** it SHALL name `cleanupPeriodDays` and its default of 30, state that clio keeps the
  indexed copy, and name `clio index --purge-missing` and `clio doctor`


## MODIFIED Requirements

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
  `install-hook`, `uninstall-hook`, and `config raw-retention` — SHALL have a
  reference entry or a cheat-sheet line

#### Scenario: Raw retention restore path is documented

- **WHEN** a user reads either README or `docs/USAGE.md`
- **THEN** it SHALL show `clio config raw-retention` and state that restoring raw_json
  under a retention window takes `clio config raw-retention off` and then
  `clio index --full`

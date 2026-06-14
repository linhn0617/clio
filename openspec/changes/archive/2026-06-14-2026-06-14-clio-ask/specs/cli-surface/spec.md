## ADDED Requirements

### Requirement: `clio ask` command

`clio ask "<question>"` SHALL print the evidence bundle for a question, grouped by
session with citations and windowed excerpts. It SHALL accept `--project`
(default: all projects), `--since`, `--limit` (max sessions), `--window <n>`
(turns each side of a hit), and `--json`. An empty question SHALL be a usage error;
a missing or empty index SHALL print an empty result and exit 0. The command SHALL
bring the index up to date with an incremental catch-up like `clio search`,
deferring to a live MCP server (opening read-only) when one is running.

#### Scenario: Ask prints a grouped, cited bundle

- **WHEN** the user runs `clio ask "database migration plan"`
- **THEN** the output SHALL group relevant excerpts by session, each with a
  citation the user can pass to `clio show`

#### Scenario: JSON output

- **WHEN** the user runs `clio ask "..." --json`
- **THEN** the bundle SHALL be emitted as JSON

#### Scenario: Empty question is a usage error

- **WHEN** the user runs `clio ask ""`
- **THEN** the command SHALL report a usage error and exit non-zero

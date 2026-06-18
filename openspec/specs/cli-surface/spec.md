# cli-surface Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.
## Requirements
### Requirement: CLI subcommands

The system SHALL expose `index`, `search`, `list`, `show`, `mcp`, `doctor`, `install-mcp`, and `uninstall-mcp` subcommands via a single `clio` binary.

#### Scenario: Help lists subcommands

- **WHEN** the user runs `clio --help`
- **THEN** the system SHALL list all subcommands with short descriptions

### Requirement: Search and show output formats

The system SHALL support human-readable and JSON output. `show` SHALL accept `--json` as an
alias for `--format json` and a `--limit` flag bounding the number of messages rendered. In
`show --format raw`, the system SHALL collapse consecutive messages that share an identical
`raw_json` into a single printed line (the multiple content blocks expanded from one source
event share one `raw_json`), so the raw dump does not repeat that event's line. Resolving a
session id SHALL treat an exact uuid match as authoritative even when it is also a prefix of
other session ids.

#### Scenario: JSON output for piping

- **WHEN** the user runs `clio search <query> --json`
- **THEN** the system SHALL emit machine-readable JSON suitable for piping

#### Scenario: Show with the --json convenience flag

- **WHEN** the user runs `clio show <id> --json`
- **THEN** the system SHALL render the session as JSON, equivalent to `--format json`

#### Scenario: Show full session

- **WHEN** the user runs `clio show <uuid-prefix>` with an unambiguous prefix
- **THEN** the system SHALL render the full session in the requested format (markdown by
  default), resolving the prefix to a single session

#### Scenario: Show message limit

- **WHEN** the user runs `clio show <id> --limit N` with N greater than 0
- **THEN** the system SHALL render at most N messages, rather than a hard-coded ceiling (a
  non-positive N falls back to the default ceiling)

#### Scenario: Raw format collapses an event's repeated line

- **WHEN** the user runs `clio show <id> --format raw` for a session whose source line
  expanded into several adjacent stored messages sharing the same `raw_json`
- **THEN** the system SHALL print that `raw_json` once for the run of identical adjacent
  lines, while adjacent messages with differing `raw_json` each print

#### Scenario: Exact id wins over prefix collisions

- **WHEN** a session uuid is an exact match for the argument and is also a prefix of other
  session uuids
- **THEN** the system SHALL resolve to the exact session rather than reporting an ambiguous
  prefix

### Requirement: CLI defers to MCP writer

The system SHALL skip its own incremental ingest when an MCP server is the active writer.

#### Scenario: MCP server running

- **WHEN** a valid MCP lock file exists and the user runs a CLI query
- **THEN** the CLI SHALL query read-only without performing incremental ingest

### Requirement: Activity filters and summary in the CLI

`clio list` and `clio search` SHALL accept `--touched <path-prefix>`, `--tool
<name>`, and `--ran <command-substring>` to filter results by indexed activity. A
new `clio activity --by file|command|tool|pattern|url [--since <when>] [--project
<prefix>]` SHALL report counts grouped by the chosen activity kind, most frequent
first.

#### Scenario: Filter sessions by touched file

- **WHEN** the user runs `clio list --touched /x/auth.ts`
- **THEN** only sessions whose tool calls touched a path under `/x/auth.ts` SHALL
  be listed

#### Scenario: Filter search by tool

- **WHEN** the user runs `clio search "race" --tool Bash`
- **THEN** only matches from sessions that ran a `Bash` tool_use SHALL be returned

#### Scenario: Activity grouped by command

- **WHEN** the user runs `clio activity --by command --since 7d`
- **THEN** the system SHALL list commands run in the last 7 days with their counts

### Requirement: Recall and hook commands

The CLI SHALL provide `clio recall` (print the current project's recall digest),
`clio install-hook` (register the SessionStart recall hook), and
`clio uninstall-hook` (remove it).

#### Scenario: recall prints the digest

- **WHEN** the user runs `clio recall` in an indexed project
- **THEN** the recent sessions / files / commands digest SHALL be printed to stdout

#### Scenario: install-hook is independent of MCP registration

- **WHEN** the user runs `clio install-hook`
- **THEN** the SessionStart hook SHALL be registered without changing the
  `~/.claude.json` MCP-server registration

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

### Requirement: `clio tui` command

`clio tui` SHALL launch the interactive dashboard. It SHALL bring the index up to
date with an incremental catch-up like `clio search` (deferring to a live MCP
server), and exit cleanly when the user quits.

#### Scenario: Launch the dashboard

- **WHEN** the user runs `clio tui` in a terminal with an index present
- **THEN** the interactive dashboard SHALL start

#### Scenario: No index

- **WHEN** `clio tui` runs with no index
- **THEN** it SHALL report that the index is missing (suggesting `clio index`) and
  exit non-zero

### Requirement: Subagent sessions are nested under their parent in the CLI

`clio list` SHALL list only top-level sessions by default, excluding subagent child
sessions (those with a populated `parent_session` whose parent session exists), and
SHALL annotate a parent that spawned subagents with a `(+N subagents)` marker. A
`--include-subagents` flag SHALL include subagent sessions in the listing. A
subagent whose parent session is not indexed SHALL still be listed (orphan
promotion), so nothing becomes invisible.

#### Scenario: Subagents are hidden from the default list

- **WHEN** the user runs `clio list` and a parent session spawned 3 subagents
- **THEN** the 3 subagent sessions SHALL NOT appear as separate rows, and the parent
  SHALL be annotated `(+3 subagents)`

#### Scenario: Subagents can be included

- **WHEN** the user runs `clio list --include-subagents`
- **THEN** subagent sessions SHALL appear in the listing

#### Scenario: An orphan subagent stays visible

- **WHEN** a subagent session's parent is not indexed
- **THEN** that subagent SHALL still be listed at top level

### Requirement: show surfaces a session's subagents

`clio show <parent>` SHALL list the parent's subagents (id, type, title) as a
drillable trailing section, and SHALL inline their transcripts when
`--include-subagents` is given. `clio show <agent-id>` SHALL render the subagent
transcript with a header identifying it as a subagent of its parent and its type.

#### Scenario: A parent lists its subagents

- **WHEN** the user runs `clio show <parent>` for a session that spawned subagents
- **THEN** the output SHALL list each subagent's id, type, and title

#### Scenario: A subagent shows its parent in a header

- **WHEN** the user runs `clio show <agent-id>`
- **THEN** the output SHALL include a header noting it is a subagent (with its type)
  of its parent session

### Requirement: search labels subagent hits

`clio search` SHALL label a result that comes from a subagent transcript with the
subagent's type, so a hit from a subagent is distinguishable from one in a
top-level conversation.

#### Scenario: A subagent hit is labeled

- **WHEN** a `clio search` hit comes from a subagent transcript of type `Explore`
- **THEN** the rendered result SHALL be marked as a subagent showing `Explore`


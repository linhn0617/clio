# cli-surface Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.
## Requirements
### Requirement: CLI subcommands

The system SHALL expose `index`, `search`, `list`, `show`, `mcp`, `doctor`, `install-mcp`, `uninstall-mcp`, and `file-history` subcommands via a single `clio` binary.

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
<path>]` SHALL report counts grouped by the chosen activity kind, most frequent
first. The `--project <path>` filter of `clio list` and `clio activity` SHALL match
sessions whose project path equals `<path>` or lies beneath it (a `/` boundary, a
trailing `/` ignored), never a sibling that merely shares the prefix.

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

#### Scenario: Project filter respects the path boundary

- **WHEN** the user runs `clio list --project /x/foo` with sessions under `/x/foo`,
  `/x/foo/sub`, and `/x/foo-bar`
- **THEN** the first two SHALL be listed and `/x/foo-bar` SHALL NOT

### Requirement: Recall and hook commands

The CLI SHALL provide `clio recall` (print the current project's recall digest,
with `--tail-runes` bounding the "last session left off" excerpt),
`clio file-history <path>` (print one file's past-session timeline; with `--hook`
it reads a PreToolUse payload from stdin instead and answers in hook JSON),
`clio install-hook` (register the SessionStart recall hook and, unless
`--no-file-context`, the PreToolUse file-history hook), and
`clio uninstall-hook` (remove both).

#### Scenario: recall prints the digest

- **WHEN** the user runs `clio recall` in an indexed project
- **THEN** the last-session excerpt (when present) followed by the recent
  sessions / files / commands digest SHALL be printed to stdout

#### Scenario: file-history prints a timeline

- **WHEN** the user runs `clio file-history <path>` for a file with indexed history
- **THEN** the sessions that touched it SHALL be printed newest first

#### Scenario: install-hook is independent of MCP registration

- **WHEN** the user runs `clio install-hook`
- **THEN** the hooks SHALL be registered without changing the
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
SHALL annotate a parent that spawned subagents with a `(+N subagents)` marker. A child
SHALL be hidden whenever its parent is indexed and passes the same filters as the listing,
independent of the page size (`--limit`): a parent ranking below the limit still hides its
children. A
`--include-subagents` flag SHALL include subagent sessions in the listing. A
subagent whose parent session is not indexed, or is excluded by the listing's own filters
(for example older than `--since`, or in another project), SHALL still be listed (orphan
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

#### Scenario: Parent beyond the page limit still hides its child

- **WHEN** `clio list --limit 1` runs, the most recent session is a subagent whose parent
  is indexed but ranks below the limit
- **THEN** the subagent SHALL NOT appear and the page SHALL show the next top-level session

#### Scenario: A parent filtered out by the listing's filters promotes the child

- **WHEN** `clio list --since 1d` runs and a recent subagent's parent is older than the cutoff
- **THEN** that subagent SHALL be listed at top level

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

### Requirement: clio ask exposes a token budget flag

`clio ask` SHALL accept a `--max-tokens` integer flag that bounds the total estimated
token size of the evidence bundle's excerpt text, mapping to the `ask` capability's
global token budget (and subject to its keep-top-hits invariant). When the flag is
omitted (or 0), the package default (2000 tokens) SHALL apply. The flag SHALL affect
both the human-readable and `--json` output.

#### Scenario: --max-tokens bounds the bundle

- **WHEN** `clio ask "<question>" --max-tokens 500 --json` runs against a large history
- **THEN** the emitted bundle's estimated token size SHALL NOT exceed the larger of 500
  and the top group's minimum-length hit excerpts, still leading with the most relevant
  session's hits

#### Scenario: Omitting the flag applies the default budget

- **WHEN** `clio ask "<question>"` runs without `--max-tokens`
- **THEN** the default budget (2000 tokens) SHALL be applied

### Requirement: usage subcommand
The CLI SHALL provide `clio usage [--since <dur>] [--project <path>] [--source <name>]
[--by session|project|model]` listing top items by total tokens. Output SHALL be sectioned per
source with per-source subtotals; no cross-source combined token total SHALL be printed (token
counts from different tokenizers are not comparable). `--by session` rows SHALL carry a session
identifier (uuid prefix) and title usable with `clio show`; `--by project` and `--by model` rows
SHALL instead carry a drill-down hint to the session-level listing. Subagent sessions appear as
their own flagged rows (no parent roll-up in this change). Token counts are displayed without
monetary conversion.

#### Scenario: Top sessions by tokens
- **WHEN** the user runs `clio usage --since 7d --by session`
- **THEN** sessions from the last 7 days are listed per source in descending token order with
  uuid prefix, title, project, and per-category totals

#### Scenario: No cross-source total
- **WHEN** the indexed data contains Claude and Codex sessions and the user runs `clio usage`
- **THEN** the output shows per-source sections and subtotals and no combined grand total

#### Scenario: Aggregate grouping offers drill-down
- **WHEN** the user runs `clio usage --by project`
- **THEN** each row shows the project's per-source totals and a drill-down invocation
  (`clio usage --project <path> --by session`) instead of a single session id

#### Scenario: Empty state guides backfill
- **WHEN** `clio usage` runs against a database indexed before this feature without a full
  reindex
- **THEN** the output explains that usage data requires `clio index --full`

### Requirement: quota listing is CLI-only with staleness
`clio usage --quota` SHALL print stored quota snapshots per the usage-summary staleness
requirement (age line, stale on window-exceeded or past `resets_at`, fixed "last-observed, not
live" disclaimer). This is the only surface for quota data in this change.

#### Scenario: Quota printed with disclaimer
- **WHEN** the user runs `clio usage --quota` with a stored snapshot
- **THEN** the output shows observation age, reset time, and the not-live disclaimer

### Requirement: list shows session tokens
`clio list` and the TUI browse/activity views SHALL display a per-session total token column
when usage data exists, and render an empty placeholder (not zero) when it does not.

#### Scenario: list column present
- **WHEN** `clio list --since 7d` runs on an indexed database with usage data
- **THEN** each session row includes its total token count

#### Scenario: Missing data renders placeholder
- **WHEN** `clio list` includes a session that has no usage row
- **THEN** that row shows a placeholder, not `0`

### Requirement: show degrades honestly on pruned sessions
`clio show --format raw` and `--format json` SHALL detect messages whose
`raw_json` is the empty pruned sentinel and render an honest signal rather than
empty lines: `--format raw` emits a per-session note that the raw form was
pruned and can be restored with `clio index --full` (and still prints any
non-pruned lines); `--format json` emits `"RawJSON": null` for pruned messages (the pre-existing capitalized message key is preserved for backward compatibility)
and a top-level `"raw_pruned": true` flag. `--format markdown` is unaffected.

#### Scenario: Raw format notes a pruned session
- **WHEN** `clio show <uuid> --format raw` targets a fully pruned session
- **THEN** the output states the raw form was pruned and names the restore
  command, rather than printing blank lines

#### Scenario: JSON format flags pruned messages
- **WHEN** `clio show <uuid> --format json` targets a session with pruned
  messages
- **THEN** those messages carry `RawJSON: null` (existing key preserved) and the payload carries
  `raw_pruned: true`

#### Scenario: Markdown is unaffected
- **WHEN** `clio show <uuid>` (markdown) targets a pruned session
- **THEN** the output is identical to before pruning (markdown never used
  `raw_json`)

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

### Requirement: Terminal escape sequences are stripped from human-readable output

Human-readable CLI output SHALL NOT replay terminal control sequences carried in
indexed text. Transcripts can contain, for example, OSC 52 clipboard writes,
window-title changes, or screen clears. The human-readable output of `clio show`
(markdown), `list`, `search`, `ask`, `activity`, `recall`, `usage`, and
`file-history` SHALL strip such sequences from every indexed string it prints
(titles, project paths, snippets, excerpts, message content, tool names, activity
values): ESC-introduced CSI, OSC, DCS, APC, PM, SOS, and other ESC sequences (an
unterminated one is dropped to the end of the string, unless a new ESC appears
first — that aborts the earlier sequence and parsing restarts there, as a terminal
does), C0 control characters other than newline and tab (carriage return included),
DEL, and C1 control characters, whether UTF-8 encoded or a raw byte outside a valid
UTF-8 sequence; a C1 CSI, OSC, DCS, SOS, PM, or APC introducer or ST acts like its
ESC form.
Ordinary text, CJK, and emoji SHALL be preserved. Machine formats are unaffected:
`--format json`, `--format raw`, `--json`, the file-history hook's JSON envelope,
and MCP responses SHALL keep the stored text, with control characters escaped by
the JSON encoder.

#### Scenario: Escape sequences in a session are not replayed by show

- **WHEN** a session message contains `ESC ] 52 ; c ; <base64> BEL` and `ESC [ 2 J`
  and the user runs `clio show <id>`
- **THEN** the output SHALL contain neither `ESC` nor `BEL`, and the surrounding
  visible text SHALL still be printed

#### Scenario: Escape sequences in a recall digest

- **WHEN** the last assistant excerpt, a session title, a touched file, or a command
  contains terminal escape sequences and `clio recall` renders the digest
- **THEN** the digest SHALL contain no `ESC` byte

#### Scenario: JSON and raw output are unchanged

- **WHEN** the user runs `clio show <id> --format json` or `--format raw` on the
  same session
- **THEN** the stored message text SHALL be emitted as stored (control characters
  JSON-escaped in `json`), not stripped

### Requirement: Ambiguous session id lists candidates

The system SHALL, when a session-id prefix matches more than one session and none
exactly, report the matching sessions so the user can retry with a longer id.

#### Scenario: Candidates are listed

- **WHEN** the user runs `clio show <prefix>` (or calls MCP `read_session`) with a
  prefix that matches several sessions and is not itself a session id
- **THEN** the error SHALL list at most 10 matching sessions, top-level sessions
  before subagents (marked as subagents), each group newest first, each with its
  start time and a single-line title without terminal escapes

#### Scenario: Listed ids resolve

- **WHEN** the user retries with an id shown in the list
- **THEN** it SHALL resolve to that session: the shown id is the shortest prefix
  (at least 8 characters, or the whole id when it is shorter) that matches no other
  session in scope and is no other session's exact id; when any matching id in scope is
  not valid UTF-8 or contains U+FFFD, every candidate is shown with its whole id

#### Scenario: More matches than listed

- **WHEN** more than 10 sessions match the prefix
- **THEN** the error SHALL list 10 of them and state that more sessions match

#### Scenario: Empty id

- **WHEN** the session id argument is empty or only whitespace
- **THEN** the system SHALL report an empty id rather than list any sessions


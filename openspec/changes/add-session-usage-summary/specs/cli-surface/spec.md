# cli-surface (delta)

## ADDED Requirements

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

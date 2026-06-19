# tui Specification

## Purpose
TBD - created by archiving change 2026-06-14-tui. Update Purpose after archive.
## Requirements
### Requirement: Interactive history dashboard

`clio tui` SHALL open an interactive full-screen dashboard over the existing index,
presenting four tabbed views — Search, Browse, Activity, and Ask — in a
master-detail layout (a result/input list with a preview pane). It SHALL be a
presentation layer over the existing data layer, adding no query logic; it SHALL
open the index read-only (after a startup catch-up), run queries asynchronously so
the interface never blocks, surface query errors without crashing, and never mutate
state.

#### Scenario: Switch between views

- **WHEN** the user presses the tab-switch key
- **THEN** the dashboard SHALL move between the Search, Browse, Activity, and Ask
  views, preserving each view's state

#### Scenario: Live search with preview

- **WHEN** the user types in the Search view
- **THEN** results SHALL update (debounced) and selecting one SHALL preview that
  session with the matched line marked

#### Scenario: Errors never crash the dashboard

- **WHEN** a query fails
- **THEN** the error SHALL be shown in the status line and the dashboard SHALL
  remain usable

#### Scenario: Quit

- **WHEN** the user presses `q` or `Ctrl-C`
- **THEN** the dashboard SHALL exit cleanly

### Requirement: The TUI nests subagents under their parent

The TUI Browse view SHALL list only top-level sessions by default and SHALL allow
expanding a parent to reveal its subagents as indented child rows (and collapsing
them again), with the parent row showing its subagent count. The Search view SHALL
mark a hit that comes from a subagent transcript (for example with a `↳` indicator
and the subagent's type). Selecting a parent MAY inline its subagents in the
preview pane.

#### Scenario: Expand a parent to see its subagents

- **WHEN** the user expands a Browse row for a session that spawned subagents
- **THEN** the subagents SHALL appear as indented child rows beneath it

#### Scenario: Collapse hides the subagents again

- **WHEN** the user collapses an expanded parent row
- **THEN** its subagent rows SHALL no longer be shown

#### Scenario: Search marks a subagent hit

- **WHEN** a Search hit comes from a subagent transcript
- **THEN** the result row SHALL be marked as a subagent (with its type)

### Requirement: The TUI is source-aware

The TUI SHALL label each session or result row with its source when sources other than
Claude Code are shown, and SHALL accept a launch-time `--source` filter (`claude-code`,
`codex`, or `all`) defaulting to `claude-code`.

#### Scenario: Default TUI shows only Claude Code

- **WHEN** the user launches `clio tui` with Codex sessions indexed
- **THEN** Browse and Search SHALL show only `claude-code` sessions

#### Scenario: Source filter at launch

- **WHEN** the user launches `clio tui --source all`
- **THEN** rows from both sources SHALL appear, each labeled with its source


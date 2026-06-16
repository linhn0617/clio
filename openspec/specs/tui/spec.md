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


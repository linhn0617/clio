## ADDED Requirements

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

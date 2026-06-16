## Why

clio's history lives behind one-shot CLI commands: `search` prints results, then
you copy a session id into `show` to read it — a slow loop for exploring. There is
no interactive way to search-as-you-type with a live preview, browse by
recency/project, skim what you worked on, or ask a question without leaving the
flow.

## What Changes

- **Added** `clio tui`: an interactive full-screen dashboard over the existing
  index, with four tabbed views — Search (live), Browse (recent / by project),
  Activity (files/commands/tools), and Ask (cited evidence bundle) — in a
  master-detail layout (list left, preview right). It is a presentation layer over
  the existing data layer (`search`, `sessions`, `ask`) and adds no query logic; it
  opens the index read-only after a startup catch-up, runs queries asynchronously
  so the UI never blocks, surfaces errors without crashing, and never mutates
  state. Built on Bubble Tea (bubbletea + bubbles + lipgloss).

## Capabilities

### Added Capabilities

- `tui`: an interactive terminal dashboard for searching, browsing, summarizing,
  and asking over indexed history, in a tabbed master-detail interface.

### Modified Capabilities

- `cli-surface`: adds the `clio tui` command.

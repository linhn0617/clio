# clio TUI — Design (interactive history dashboard)

- **Date:** 2026-06-14
- **Status:** Approved (brainstorming) — to be implemented via OpenSpec SDD + TDD, then codex + `/review`.
- **Origin:** roadmap feature #4, after #1 activity index (v0.4.0), #2 ambient-recall hook (v0.5.0), #3 `clio ask` (v0.6.0).

## Decisions (locked in brainstorming)

1. **Full dashboard, four views** — Search, Browse, Activity, Ask — not a minimal
   search-only browser.
2. **Bubble Tea** (charmbracelet) as the TUI framework, over tview or raw tcell.
   Chosen for its testable Elm-style `Update(msg) → (model, cmd)` (fits clio's TDD
   culture) and idiomatic composition. *Cost:* adds bubbletea + bubbles + lipgloss
   (direct deps 4 → 7) and a few MB to the single `clio` binary.
3. **Layout = top tab bar + master-detail** (result/input list on the left, a
   preview pane on the right), consistent across all four tabs.

## Surface

- New command **`clio tui`**: an interactive dashboard over the existing index.
  One more subcommand on the single `clio` binary.

## Architecture (`internal/tui`)

- A root model holds the active tab and a shared preview component; beneath it sit
  four sub-models — `searchView`, `browseView`, `activityView`, `askView` — each a
  `tea.Model`. Composition (not one monolithic model + a big mode switch) so each
  view has clear boundaries and an independently testable `Update`.
- **Presentation layer only.** Every view reuses the existing, already-tested data
  layer; the TUI adds no query logic:

| Tab | List (left) | Preview (right) |
|---|---|---|
| **Search** | `search.Search` live results | the hit session's messages (`sessions.GetMessages`), matched line highlighted |
| **Browse** | `sessions.ListSessions` (recent / `--project`) | same |
| **Activity** | `sessions.ActivityByKind` (files / commands / tools / …) | selecting an entry → its sessions (`Touched` / `Ran` filter) |
| **Ask** | question input + `ask.Ask` evidence bundle (grouped by session) | the selected group's windowed excerpts |

## Layout & keybindings

Top tab bar (`Tab` / `Shift-Tab`, or `1`–`4`, to switch). Left list navigated with
`↑↓` / `j k`; `Enter` opens/expands; `/` focuses the search input; `?` help;
`q` / `Ctrl-C` quits. A bottom status line shows keybindings and any error.

```
 clio  [Search] Browse  Activity  Ask
 > auth bug_
 ───────────────────┬──────────────────
 ›6f2a Auth fix    ▸│ 6f2a · myapp · 05-14
  1d3c DB migrate   │ user: auth bug repro…
  88ff Rate limit   │ asst: token refresh…
  …                 │ » matched line
 ───────────────────┴──────────────────
 ↑↓ move  ⏎ open  ⇥ tab  / search  q quit
```

## Data access

- At startup, run one incremental catch-up like `clio search` (`openForQuery`,
  which opens read-only and defers to a live MCP server); afterward all queries
  read through that handle. The TUI never mutates state after startup.
- Queries run as asynchronous bubbletea `Cmd`s so the UI never blocks; live search
  is debounced (~200 ms) so it doesn't query on every keystroke.

## Error handling

Query errors surface in the status line and never crash the TUI. Each view has a
"no results" empty state; a missing index prompts the user to run `clio index`.

## Testing (TDD)

The focus is each sub-model's `Update(msg) → model` transitions — switching tabs,
moving the selection, typing, the debounce→query message flow, and rendering empty
vs. populated states — driven against the existing temp-DB fixtures (real data
layer), without driving a real terminal. The data layer itself is already tested.

## Dependencies

Adds `github.com/charmbracelet/bubbletea`, `.../bubbles`, `.../lipgloss` (plus
transitive deps); direct deps go 4 → 7 and the binary grows a few MB. This is the
accepted cost of the Bubble Tea decision.

## OpenSpec change

`2026-06-14-tui`: **ADD** a `tui` capability (the interactive dashboard and its
four views), **MODIFY** `cli-surface` (the new `clio tui` command).

## Build order (phasing within v1)

Frame (tab bar + master-detail + preview + status line) → Search → Browse →
Activity → Ask, each a red→green TDD slice.

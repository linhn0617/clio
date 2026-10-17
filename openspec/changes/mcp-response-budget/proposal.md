## Why

The MCP tools cap row counts (`limit`) but not the size of what they return. A few long
messages or wide rows can push one response past 50,000 characters; Claude Code then writes the
output to a file and hands the model only the path, costing a second read. Separately,
`read_session` can only page forward from an `offset`, so to see how a session ended the model
must first learn the total message count.

## What Changes

- **mcp-server (budget)**: every tool (`search`, `ask`, `list_sessions`, `activity_summary`,
  `read_session`) bounds its serialized JSON response to `max_chars` (default 40,000 — below
  Claude Code's 50,000 spill threshold; values are clamped to 4,000–48,000). When the full
  response is larger, rows are dropped from the end at row boundaries (search hits, session
  rows, activity rows/buckets, `ask` excerpts, `read_session` messages) until it fits, and the
  response carries `truncated: true` and a `next` hint. Size is measured on the real serialized
  output with a shrink-and-re-render search, not estimated. At least one row is always returned,
  flagged `truncated` when that one row alone exceeds the budget.
- **mcp-server (read_session)**: new `last_n` (the last N messages) and `around_sequence`
  (a window centred on a message `seq`, `limit/2` each side) parameters. `offset`, `last_n`
  and `around_sequence` are mutually exclusive; combining them is a tool error. The response now
  also reports `total`, and `offset` is the real start of the page in every mode. A truncated
  page adds `next_offset`.

## Impact

- Responses that already fit are unchanged except for the new `total` field on `read_session`.
- Clients that echo schema defaults (`offset: 0`) are not affected: `offset: 0` does not count as
  a mode.
- New `sessions.CountMessages` helper (same role filter as `GetMessages`).

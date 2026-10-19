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
  output in UTF-16 code units (how JavaScript, and so most likely Claude Code's threshold,
  counts length: an emoji counts two) with a shrink-and-re-render search, not estimated; the
  result is a single text content (no duplicate `structuredContent`). When one row alone
  exceeds the budget its free text is cut to a prefix with a marker; when its fixed fields alone
  do, no rows are returned and `next` says so. `ask` keeps hit excerpts over context;
  `read_session` caps its subagent list, shrinks the parent page before dropping inlined
  subagent messages, and keeps the target (`around_sequence`) or the tail (`last_n`).
- **mcp-server (read_session)**: new `last_n` (the last N messages) and `around_sequence`
  (a window centred on a message `seq`, backfilled to a full page near the end) parameters. `offset`, `last_n`
  and `around_sequence` are mutually exclusive; combining them is a tool error. The response now
  also reports `total`, and `offset` is the real start of the page in every mode, read from
  one snapshot with the page. A truncated page adds `next_offset`; a target hidden by the
  tool-output filter adds a `note`.

## Impact

- Responses that already fit are unchanged except for the new `total` field on `read_session`.
- Clients that echo schema defaults (`offset: 0`) are not affected: `offset: 0` does not count as
  a mode.
- New `sessions.CountMessages` and `sessions.MessageRole` helpers; `GetMessages`,
  `CountMessages` and `MaxSeq` take a `sessions.Querier` (a `*db.DB` or a `*sql.Tx`), so
  existing callers are unchanged.

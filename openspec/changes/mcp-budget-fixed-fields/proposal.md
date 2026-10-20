## Why

PR #53 holds MCP responses within `max_chars` by dropping rows and cutting row
text, but fields outside the rows were never cut. When they alone exceeded the
budget the response still overflowed, and the spec allowed it as an exception:
at `max_chars: 4000`, `ask` with the question `"authentication failure "` × 300
(6,900 characters) sent 7,065 UTF-16 units with no groups, and `read_session`
for a session whose project path is 6,000 characters sent 6,357 units.

## What Changes

- **mcp-server**: a new step in `fitBudget`, before the subagent shed and
  before any row is dropped. The fields outside the rows are the top-level keys,
  other than lists (read_session's subagents keep their own shedding) and
  `next`, whose value is the same with no rows and with all of them. Their
  target is floor (their size with every cuttable string at its marker) plus a
  quarter of the rest of the budget; when they exceed it, their strings are cut
  by `capText` to one common cap, the largest that brings them within it (binary
  search). With short ids that is about a quarter of the budget; a long id
  raises the target instead of starving the rows. read_session's subagent list is held to half the
  budget measured with the cut applied (`fitBudget` now hands the shed a
  `zeroSize` function, since measuring `render` itself would see the uncut
  path), so the list keeps at least the rest of that half and the rows the rest: an
  oversized `ask` question still returns its evidence groups, and a session
  with an oversized path still returns its messages and as many subagents as
  fit (on a tight budget the list can be shorter than for the same session
  with a short path).
- The fields are measured on their own, and a string is replaced only when the
  cut changes it and is shorter as sent (JSON-escaped, in UTF-16 units), so that
  size never shrinks as the cap grows and the search finds the largest cap. The
  cut is re-applied on every render, so later steps are never overwritten.
- Ids (keys containing `uuid`, `id`, `parent_session`, any case, including the
  elements of a list held by such a key), `note` and `next` are never cut, so a
  caller can still follow "read each by its uuid". Timestamps (`*_at`,
  `timestamp`) are never cut either. Because the target is floor plus a share
  of what floor leaves, rows and hints keep three quarters of that rest. Known
  limit: when floor comes within the size of the `next` hints (about 320 UTF-16
  units) of the budget, the response can still exceed it; that takes ids or
  notes thousands of characters long (clio's ids are 36), and the same range
  overflowed before this change.
- The cut streams the JSON tokens and writes every token it does not cut back
  byte for byte (whitespace between tokens is dropped, which json.Marshal output
  never has), so key order, numbers (int64 included) and escapes such as the
  `\ufffd` json.Marshal writes for invalid UTF-8 are kept exactly.
- `next` keeps every reason and names the cut fields
  ("long fields were cut to fit max_chars=4000: session.project") instead of
  advising to narrow a query that has nothing to narrow.

## Impact

- `internal/mcp/budget.go`, read_session's shed in `internal/mcp/tools.go`, the
  `mcp-server` spec. No schema or parameter change.
- A row whose own fields exceed the budget (a `list_sessions` or `ask` row
  carrying a 6,000-character project path) is unchanged: no rows are returned and
  `next` says the fixed fields exceed `max_chars`, as before.

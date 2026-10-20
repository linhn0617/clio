## Why

PR #53 holds MCP responses within `max_chars` by dropping rows and cutting row
text, but fields outside the rows were never cut. When they alone exceed the
budget the response still overflowed, and the spec allowed it as an exception:
at `max_chars: 4000`, `ask` with the question `"authentication failure "` × 300
(6,900 characters) sent 7,065 UTF-16 units with no groups, and `read_session`
for a session whose project path is 6,000 characters sent 6,357 units.

## What Changes

- **mcp-server**: a fifth fallback step in `fitBudget`. When even zero rows
  overflow, every string field of the payload is cut by `capText` to one common
  cap, the largest that fits (binary search), so the longest fields are cut first.
  A string is replaced only when its cut form is shorter as sent (JSON-escaped,
  in UTF-16 units); otherwise a short string would grow into a longer marker and
  a smaller cap could give a larger payload. With that rule the size never
  shrinks as the cap grows, so the search finds the largest cap; a short field
  such as an id is cut only if the cap falls below its length. `next` keeps every
  reason, including one render set itself (read_session's subagent note), then
  the fixed-fields hint and "long fields were cut to fit".
- If even the smallest cap overflowed (only possible if numbers, keys and short
  strings alone exceed the budget, which no clio zero-row payload does at
  `max_chars` ≥ 4,000), that smallest form is sent rather than an uncut one.
- The cut runs on a JSON round trip of the payload (numbers decoded exactly), so
  struct-valued fields such as `read_session`'s session are covered too. It only
  runs on this last-resort path; every other response is serialized as before.
- The spec's exception sentence is replaced by the guarantee.

## Impact

- `internal/mcp/budget.go`, the `mcp-server` spec. No schema or parameter change.

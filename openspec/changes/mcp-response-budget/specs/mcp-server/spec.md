## MODIFIED Requirements

### Requirement: stdio MCP server with four tools

The system SHALL run a stdio MCP server exposing `search`, `list_sessions`,
`activity_summary`, and `read_session` tools. When `activity_summary` groups by day, it
SHALL bucket by the local calendar day. The system SHALL validate tool inputs at the
request boundary and return a clear tool error for an unsupported value rather than a
generic downstream failure. `read_session` SHALL select its page by exactly one of three
mutually exclusive modes: `offset` (forward from a message offset, the default), `last_n`
(the last `min(last_n, limit)` messages; `last_n` SHALL be a positive integer when present),
or `around_sequence` (a window of `limit` messages centred on that message `seq` — `limit/2`
before it, the rest from it on — slid back so that a window near the end of the session is
still a full page; `around_sequence` SHALL be an integer between 0 and the session's highest
seq, otherwise a tool error). Supplying more than one mode SHALL return a tool error naming
all three parameters; an `offset` of 0 SHALL count as not supplied, while `last_n` and
`around_sequence` count as supplied whenever present (including 0). In every mode the
response SHALL report `offset` (the zero-based start of the returned page), `total` (the
session's message count under the same tool-output filter) and `has_more`.

#### Scenario: Search tool call

- **WHEN** an MCP client calls `search` with a query
- **THEN** the system SHALL return ranked results with snippets, respecting the requested
  limit

#### Scenario: Read session with pagination

- **WHEN** an MCP client calls `read_session` for a long session
- **THEN** the system SHALL return a page of messages (default excluding tool output) and
  a `has_more` flag

#### Scenario: Read the end of a session

- **WHEN** an MCP client calls `read_session` with `last_n: 5` on a 30-message session
- **THEN** the system SHALL return the last five messages with `offset: 25`, `total: 30`
  and `has_more: false`

#### Scenario: Read around a message

- **WHEN** an MCP client calls `read_session` with `around_sequence: 50` and `limit: 10` on a
  session whose seqs are 0..99
- **THEN** the system SHALL return the messages with seq 45 through 54 and `offset: 45`

#### Scenario: Conflicting read modes are rejected

- **WHEN** an MCP client calls `read_session` with both `last_n` and `around_sequence` (or
  either with a non-zero `offset`)
- **THEN** the system SHALL return a tool error naming `offset`, `last_n` and
  `around_sequence`, without reading messages

#### Scenario: Activity summary groups by local day

- **WHEN** an MCP client calls `activity_summary` with `group_by: "day"`
- **THEN** the system SHALL bucket each session by its local calendar day (not the UTC
  day)

#### Scenario: Unsupported group_by is rejected at the boundary

- **WHEN** an MCP client calls `activity_summary` with a `group_by` other than `day` or
  `project`
- **THEN** the system SHALL return a tool error naming the allowed values, without running
  the query

### Requirement: Context-overflow protection

The system SHALL clamp result sizes to protect the caller's context window. Beyond row
counts, every MCP tool (`search`, `ask`, `list_sessions`, `activity_summary`,
`read_session`) SHALL bound the serialized JSON length of its response (escapes included) to
a character budget: the `max_chars` parameter, default 40,000 (below Claude Code's 50,000
character threshold for spilling tool output to a file), clamped into 4,000–48,000, counted in
characters (Unicode code points) of the final response the client receives, which SHALL be a
single text content with no duplicate structured copy. When the full response exceeds the
budget the system SHALL drop rows at row boundaries (search hits, session rows, activity rows
or buckets, `ask` excerpts with sessions left empty omitted, `read_session` messages) until
the re-serialized response fits, and SHALL set `truncated: true` and a non-empty `next` hint.
`ask` SHALL drop context excerpts before `is_hit` excerpts and whole trailing sessions last,
so every kept session retains a hit. `read_session` SHALL shed inlined subagent messages
before any parent message and cut the subagent list to half the budget, setting
`truncated: true` with a note when it does; when it drops parent messages it SHALL drop from
the end of a forward page, from the start for `last_n` (the end of the session survives) and
from both ends for `around_sequence` (the target survives), setting `has_more`, `offset` (the
first returned message) and, when later messages were dropped, `next_offset`. The size SHALL be
measured on the real serialized output, not estimated. The response SHALL keep at least one
row: when a single row alone exceeds the budget its free text (message content, excerpt text,
snippet, title, activity value) SHALL be cut to a prefix ending in `… [truncated N chars]`
with `truncated: true`; when the fixed fields alone exceed the budget the row SHALL be returned
anyway and `next` SHALL say the fixed fields exceed `max_chars`. A response within budget SHALL
carry neither `truncated` nor `next`.

#### Scenario: Excessive limit requested

- **WHEN** an MCP client requests a limit above the maximum
- **THEN** the system SHALL clamp the limit to 50

#### Scenario: Response over the character budget

- **WHEN** an MCP client calls `list_sessions` with `limit: 50` and `max_chars: 4000` and the
  50 rows serialize to more than 4,000 characters
- **THEN** the system SHALL return fewer rows whose serialized response is at most 4,000
  characters, with `count` equal to the rows returned, `truncated: true` and a `next` hint

#### Scenario: max_chars is clamped

- **WHEN** an MCP client passes `max_chars` below 4,000 or above 48,000
- **THEN** the system SHALL use 4,000 or 48,000 respectively

#### Scenario: A single row exceeds the budget

- **WHEN** a `read_session` page holds one 120,000-character message and the budget is 40,000
- **THEN** the system SHALL return that message with its content cut to a prefix plus a
  marker, the whole response within 40,000 characters, and `truncated: true`

#### Scenario: ask keeps its hits

- **WHEN** an `ask` bundle exceeds the budget
- **THEN** the system SHALL drop context excerpts and trailing sessions while every kept
  session still contains at least one `is_hit` excerpt

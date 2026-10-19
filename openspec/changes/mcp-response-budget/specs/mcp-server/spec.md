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
session's message count under the same tool-output filter) and `has_more`; the count and the
page SHALL be read from one consistent snapshot, so a message written meanwhile cannot shift
the page. `last_n` and `around_sequence` SHALL accept the same number forms as `offset` and
`limit` (a JSON number or a numeric string); a fractional number is a tool error. When
`around_sequence` names a message hidden by the tool-output filter (tool output or thinking
while `include_tool_output` is false), the window SHALL be centred on the next visible message
(ending at the last one when none follows, empty when the session has none) and the response
SHALL carry a `note` naming the hidden message's seq and role and saying which of these
applies. `around_sequence` on a session with no messages SHALL be a tool error saying so.

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

#### Scenario: Read around a hidden tool result

- **WHEN** an MCP client calls `read_session` with `around_sequence` naming a `tool_result`
  message and without `include_tool_output`
- **THEN** the system SHALL return the window around the next dialogue message with a `note`
  saying that seq is a hidden `tool_result`

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
UTF-16 code units (so a character outside the Basic Multilingual Plane, such as most emoji,
counts as two) of the final response the client receives, which SHALL be a single text content
with no duplicate structured copy. When the full response exceeds the budget the system SHALL
drop rows at row boundaries (search hits, session rows, activity rows or buckets, `ask`
excerpts with sessions left empty omitted, `read_session` messages) until the re-serialized
response fits, and SHALL set `truncated: true` and a non-empty `next` hint that names every
reason the response was cut (rows dropped, row text cut, subagents shed), none overwriting
another. `ask` SHALL drop context excerpts before `is_hit` excerpts and whole trailing sessions
last, so every kept session retains a hit. When a `read_session` response exceeds the budget,
the subagent list SHALL be cut to half the budget, measured on its entries without inlined
messages; inlined subagent messages SHALL be kept while at least one parent message fits
beside them (measured on the final text, hints included), the parent page shrinking first, and SHALL be dropped (each subagent still listed
with `has_more`) only when even one parent message does not fit; each of these SHALL set
`truncated: true` with a note. A response that fits SHALL keep every subagent. When
`read_session` drops parent messages it SHALL drop from the end of a forward page, from the
start for `last_n` (the end of the session survives) and from both ends for `around_sequence`
(the target survives), setting `has_more`, `offset` (the first returned message) and, when
later messages were dropped, `next_offset`. The size SHALL be measured on the real serialized
output, not estimated. When a single row alone exceeds the budget its free text (message
content, excerpt text, snippet, title, activity value) SHALL be cut to a prefix ending in
`… [truncated N chars]` with `truncated: true`. When a single row's fixed fields (those with no
free text to cut) alone exceed the budget, no rows SHALL be returned and `next` SHALL say the
fixed fields exceed `max_chars`; the response then still fits unless the fields outside the
rows alone exceed the budget. A response within budget SHALL carry neither `truncated` nor
`next`.

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

#### Scenario: The budget counts UTF-16 code units

- **WHEN** a `read_session` page holds messages made of emoji and the budget is 4,000
- **THEN** the system SHALL keep the response within 4,000 UTF-16 code units (each emoji
  counting two), cutting a lone oversized message on a character boundary

#### Scenario: A single row's fixed fields exceed the budget

- **WHEN** an MCP client calls `list_sessions` with `max_chars: 4000` and the only session's
  project path is 6,000 characters long
- **THEN** the system SHALL return no session rows, `truncated: true`, a `next` hint saying the
  fixed fields exceed `max_chars`, and a response within 4,000 characters

#### Scenario: Subagent messages survive a shorter parent page

- **WHEN** a `read_session` call with `include_subagents` exceeds the budget and one parent
  message fits beside the inlined subagent messages
- **THEN** the system SHALL keep the subagent messages and return fewer parent messages with
  `next_offset`

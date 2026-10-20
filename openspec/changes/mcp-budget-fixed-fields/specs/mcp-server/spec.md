## MODIFIED Requirements

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
fixed fields exceed `max_chars`. When the fields outside the rows alone still exceed the
budget (an echoed `ask` question, a session's path or title), the system SHALL cut string
fields with the same marker to the largest common length that fits, leaving whole any string
the cut would not shorten, so the longest fields are cut first and the response is within the
budget whenever the content other than long strings fits (every clio zero-row payload does at
the minimum `max_chars`). A response within budget
SHALL carry neither `truncated` nor `next`.

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

#### Scenario: Fields outside the rows exceed the budget

- **WHEN** an MCP client calls `read_session` with `max_chars: 4000` for a session whose
  project path is 6,000 characters long, or `ask` with a question longer than the budget
- **THEN** the system SHALL return a response within 4,000 characters with `truncated: true`,
  the long field cut to a prefix ending in `… [truncated N chars]`, the session uuid whole,
  and a `next` hint saying the fixed fields exceed `max_chars`

#### Scenario: Subagent messages survive a shorter parent page

- **WHEN** a `read_session` call with `include_subagents` exceeds the budget and one parent
  message fits beside the inlined subagent messages
- **THEN** the system SHALL keep the subagent messages and return fewer parent messages with
  `next_offset`

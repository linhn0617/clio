# mcp-server Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.
## Requirements
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

### Requirement: stdout purity

The system SHALL emit only JSON-RPC on stdout; all logs and errors go to stderr.

#### Scenario: Error during a tool call

- **WHEN** a tool handler encounters an error
- **THEN** the system SHALL return an MCP error response and write diagnostics to stderr, never to stdout

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
fixed fields exceed `max_chars`. Before the subagent list is shed and before any row is
dropped, the system SHALL cut the strings of the fields outside the rows other than lists (an
echoed `ask` question, a session's path or title) with the same marker to the largest common
length that brings those fields within their uncuttable size plus a quarter of the rest of
the budget, when they exceed it, leaving whole ids (`uuid` keys, `id`, `parent_session`, list elements under
such keys), timestamps (`*_at`, `timestamp`), notes and hints and any string the cut would not
shorten, and sending the smallest form when the uncuttable part alone fills the budget; `next`
SHALL name each cut field once; the subagent
list's half-budget check SHALL measure the payload with that cut applied. A response within budget SHALL carry neither
`truncated` nor `next`.

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
  the session's messages (or the evidence groups) and its subagents that fit beside it, and
  a `next` hint naming the cut field

#### Scenario: Subagent messages survive a shorter parent page

- **WHEN** a `read_session` call with `include_subagents` exceeds the budget and one parent
  message fits beside the inlined subagent messages
- **THEN** the system SHALL keep the subagent messages and return fewer parent messages with
  `next_offset`

### Requirement: Tools advertise MCP safety annotations

The system SHALL attach MCP `ToolAnnotation` hints to every registered tool so
clients can render accurate trust labels instead of falling back to the spec's
worst-case default of destructive + open-world. Each of `search`,
`list_sessions`, `activity_summary`, and `read_session` SHALL advertise
`readOnlyHint=true`, `destructiveHint=false`, `idempotentHint=true`, and
`openWorldHint=false`.

#### Scenario: Client lists tools

- **WHEN** an MCP client calls `tools/list`
- **THEN** each of the four clio tools SHALL include an `annotations` object
  with `readOnlyHint=true`, `destructiveHint=false`, `idempotentHint=true`, and
  `openWorldHint=false`

#### Scenario: Annotations match implementation reality

- **WHEN** any clio tool handler runs
- **THEN** it SHALL only read from the local session index — never mutate state,
  delete data, or contact an external service — so the advertised
  read-only/non-destructive/closed-world hints remain truthful

### Requirement: Activity-aware MCP queries

`list_sessions` SHALL accept `touched` (path prefix), `tool` (name), and `ran`
(command substring) filters, and `activity_summary`'s `group_by` SHALL additionally
accept `file`, `command`, `tool`, `pattern`, and `url`, so Claude can answer activity
questions over the indexed history.

#### Scenario: list_sessions filtered by tool

- **WHEN** an MCP client calls `list_sessions` with `tool="Bash"`
- **THEN** only sessions that ran a `Bash` tool_use SHALL be returned

#### Scenario: list_sessions filtered by touched path

- **WHEN** an MCP client calls `list_sessions` with `touched="/x/auth"`
- **THEN** only sessions whose tool calls touched a path under `/x/auth` SHALL be
  returned

#### Scenario: activity_summary grouped by file

- **WHEN** an MCP client calls `activity_summary` with `group_by="file"`
- **THEN** the system SHALL return counts of activity grouped by file path

### Requirement: `ask` MCP tool

The MCP server SHALL expose a read-only `ask` tool taking `question` (required) and
optional `project`, `since`, and `limit`, returning the evidence bundle as
structured JSON so the client can synthesize an answer and cite session ids. The
tool SHALL be annotated read-only and SHALL perform no generation.

#### Scenario: Ask tool returns a structured bundle

- **WHEN** the client calls `ask` with a question
- **THEN** the server SHALL return grouped, cited excerpts as JSON

#### Scenario: Ask tool is annotated read-only

- **WHEN** the tool list is inspected
- **THEN** `ask` SHALL carry a read-only annotation

### Requirement: MCP exposes subagents as parent-linked children

The MCP `list_sessions` tool SHALL exclude subagent child sessions by default and
accept an `include_subagents` parameter to include them. `read_session` on a parent
SHALL report its subagents (id, type, title), optionally inlining their messages.
`search` results SHALL carry each hit's `parent_session` and `agent_type` so a
client can attribute a hit to a subagent and its parent. All three SHALL remain
read-only.

#### Scenario: list_sessions hides subagents by default

- **WHEN** a client calls `list_sessions` with no `include_subagents`
- **THEN** subagent child sessions SHALL be omitted from the result

#### Scenario: read_session reports a parent's subagents

- **WHEN** a client calls `read_session` on a parent that spawned subagents
- **THEN** the result SHALL include the parent's subagents

#### Scenario: read_session inlines subagent messages on request

- **WHEN** a client calls `read_session` on a parent with `include_subagents` set
- **THEN** each reported subagent SHALL include its own messages

#### Scenario: search hits carry parent and type

- **WHEN** a `search` hit comes from a subagent transcript
- **THEN** the result SHALL include its `parent_session` and `agent_type`

### Requirement: MCP read tools are source-filtered

The MCP read tools SHALL accept a `source` parameter whose enumerated values are the
registered source names plus `all`, **derived from the source registry**, defaulting to
`claude-code` — namely `search`, `ask`, `list_sessions`, `activity_summary`, and
`read_session` — so an MCP client sees only Claude Code history unless it opts in. Neither
the enum nor the default SHALL be hardcoded per tool; both SHALL come from the registry
(default = the fallback source). Results SHALL carry each session's source. The tools SHALL
remain read-only.

#### Scenario: list_sessions defaults to Claude Code

- **WHEN** an MCP client calls `list_sessions` with no `source` argument
- **THEN** only `claude-code` sessions SHALL be returned

#### Scenario: Codex sessions via MCP are opt-in

- **WHEN** an MCP client calls `search` with `source` `codex`
- **THEN** only `codex` results SHALL be returned, each carrying its source

#### Scenario: Every read tool's enum tracks the registry

- **WHEN** the registry contains only `claude-code` and `codex`
- **THEN** the `source` enum on all five read tools SHALL be exactly `claude-code`,
  `codex`, `all` with default `claude-code`, derived from the registry rather than written
  out per tool

### Requirement: The ask tool exposes a token budget parameter

The read-only `ask` MCP tool SHALL accept an optional `max_tokens` number parameter that
bounds the total estimated token size of the returned evidence bundle's excerpt text,
subject to the `ask` capability's keep-top-hits invariant (the top group's
minimum-length hit excerpts may exceed a very small budget). It SHALL default to 2000
and be clamped to a safe range (minimum 200, maximum 8000). When omitted, the default
SHALL apply. The parameter SHALL map to the `ask` capability's global token budget; all
other `ask` tool parameters SHALL be unchanged.

#### Scenario: max_tokens bounds the returned bundle

- **WHEN** an MCP `ask` call sets `max_tokens` to 500
- **THEN** the returned bundle's estimated token size SHALL NOT exceed the larger of
  500 and the top group's minimum-length hit excerpts, while still leading with the
  most relevant session's hits

#### Scenario: Omitting max_tokens applies the default

- **WHEN** an MCP `ask` call does not set `max_tokens`
- **THEN** the default budget (2000 tokens) SHALL be applied

#### Scenario: Out-of-range max_tokens is clamped

- **WHEN** an MCP `ask` call sets `max_tokens` above the maximum or below the minimum
- **THEN** the value SHALL be clamped into the allowed range rather than rejected

### Requirement: activity_summary usage dimension
The MCP `activity_summary` tool SHALL accept `group_by: "usage"` returning session-level token
aggregates (session uuid, title, project, source, per-category totals) so the MCP client can
follow up with `read_session`. Results SHALL be attributed per source and no cross-source
combined total SHALL be returned. Responses expose token aggregates only; no monetary amounts.

#### Scenario: MCP client ranks sessions by tokens
- **WHEN** an MCP client calls `activity_summary` with `group_by: "usage"` and a period
- **THEN** it receives sessions ranked by total tokens within each source, with identifiers
  accepted by `read_session`

### Requirement: Quota data never crosses MCP
The MCP server SHALL NOT include quota snapshot fields (used_percent, resets_at, window
identity, plan_type, credits, or any account-level quota data) in any tool response. There is no configuration
override in this change; quota data is CLI-only. Any future quota-over-MCP exposure requires its
own change with a real configuration design.

#### Scenario: No quota field over MCP
- **WHEN** an MCP client calls any clio tool, including `activity_summary` with
  `group_by: "usage"`
- **THEN** no quota snapshot field appears anywhere in the response


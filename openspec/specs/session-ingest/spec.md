# session-ingest Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.
## Requirements
### Requirement: Scan and ingest Claude Code session files

The system SHALL scan `~/.claude/projects/**/*.jsonl` and ingest each session's events into SQLite.

#### Scenario: Full ingest of a project directory

- **WHEN** `clio index` runs against `~/.claude/projects/`
- **THEN** the system SHALL walk every `.jsonl` file, parse each line as an event, and UPSERT one row per session into `sessions` and one row per message into `messages`, populating the FTS index for searchable content

### Requirement: Project path derived from event cwd

The system SHALL determine a session's project path from the first event containing a `cwd` field, NOT by decoding the encoded directory name.

#### Scenario: Path with underscores and hyphens

- **WHEN** a session file lives under `-Users-lin-Herd-cli-project-COMPLETE` and its events contain `"cwd":"/Users/lin/Herd/cli_project_COMPLETE"`
- **THEN** the system SHALL store `project_path` as `/Users/lin/Herd/cli_project_COMPLETE`, using the directory name only as a fallback when no event carries `cwd`

### Requirement: Incremental append-aware ingest

The system SHALL re-ingest only newly appended content using a last-complete-newline
offset plus head and tail fingerprint checks, SHALL stream the unread tail with bounded
memory, and SHALL never silently drop a complete line it cannot parse.

#### Scenario: File grew by a partial line

- **WHEN** a `.jsonl` file has grown since last ingest and its tail ends mid-line
- **THEN** the system SHALL parse only up to the last complete newline, persist that
  offset as `last_byte_offset`, and leave the partial trailing bytes for the next run

#### Scenario: Same-size rewrite detected

- **WHEN** a file's size and mtime suggest no change but its tail fingerprint differs
  from the stored fingerprint
- **THEN** the system SHALL treat the file as changed and re-ingest

#### Scenario: Pre-watermark rewrite detected by head fingerprint

- **WHEN** a file is rewritten so that its leading bytes differ from the stored head
  fingerprint, even if the tail bytes at the stored offset are unchanged
- **THEN** the system SHALL fall back to a full reingest rather than resuming an append

#### Scenario: Large unread tail is read with bounded memory

- **WHEN** the unread tail between the watermark and EOF is large
- **THEN** the system SHALL stream complete lines without loading the entire tail into
  memory at once, while preserving single-transaction-per-file commit

#### Scenario: Complete line that cannot be parsed

- **WHEN** ingest reads a complete (newline-terminated) line that fails to parse
- **THEN** the system SHALL skip that line, increment a persistent per-source
  `unparsed_lines` counter, and still advance the watermark so the failure does not
  block later lines in the same file

### Requirement: Secret redaction during ingest

The system SHALL redact secret patterns before persisting content, covering both
free-text values (via shape patterns, including `Authorization: Basic` and
`Cookie`/`Set-Cookie` headers) and structured JSON (via key-name matching, including
the `authorization` and `cookie` keys), and SHALL ensure no secret reaches the
session title.

#### Scenario: Shape-pattern secret in free text

- **WHEN** a message body contains a recognizable secret (e.g. `sk-…`, a JWT, an AWS
  access key id, a `Bearer <token>`, or a `KEY=value` env line)
- **THEN** the system SHALL replace it with a `[REDACTED:<type>]` marker in both the
  searchable `content` and the stored `raw_json`

#### Scenario: Basic auth header in free text

- **WHEN** content contains an `Authorization: Basic <base64>` header (or a bare
  `Basic <base64>` credential)
- **THEN** the system SHALL replace the credential with `Basic [REDACTED:token]`,
  leaving the prose word "basic" untouched

#### Scenario: Cookie header value

- **WHEN** content contains a `Cookie:` or `Set-Cookie:` header line
- **THEN** the system SHALL replace its value with `[REDACTED:cookie]`, leaving a
  sentence that merely mentions the word "cookie" untouched

#### Scenario: Connection string with embedded credentials

- **WHEN** content contains a credentialed connection string such as
  `postgres://user:pass@host/db`
- **THEN** the system SHALL replace it with `[REDACTED:connstring]`, while leaving
  credential-less URLs (e.g. `https://example.com`) untouched

#### Scenario: Secret under a structured JSON key

- **WHEN** a session event's JSON contains a string value under a secret-bearing key
  (e.g. `{"apiKey":"…"}`, `{"token":"…"}`, `{"db_password":"…"}`, `{"authorization":"…"}`,
  `{"cookie":"…"}`)
- **THEN** the system SHALL replace that value with `[REDACTED:key]` in the stored
  `raw_json`, regardless of whether the value matches a shape pattern

#### Scenario: Secret embedded as JSON inside a text field

- **WHEN** a message body is (or contains) JSON text such as `{"apiKey":"…"}`
- **THEN** the system SHALL parse that JSON, replace the secret-keyed value with
  `[REDACTED:key]`, and store the redacted form in `content` and `raw_json`

#### Scenario: Whole value redacted regardless of type under a secret key

- **WHEN** a secret-bearing key holds a non-string value (number, array, or object)
  such as `{"auth_token":{"u":"a"}}` or `{"set-cookie":["a","b"]}`
- **THEN** the system SHALL redact the entire value, not only string leaves

#### Scenario: Session title never contains a raw secret

- **WHEN** the first user message used to derive a session's title contains a secret
- **THEN** the stored `sessions.title` SHALL contain only the redacted form, never the
  raw secret

#### Scenario: raw_json fidelity is display-grade, not byte-exact

- **WHEN** redaction re-serializes an event's JSON for `raw_json`
- **THEN** the system SHALL preserve values including `<`, `>`, `&` and large
  integers, while object key order and insignificant whitespace MAY be normalized
  (raw_json is for display via `clio show --format raw`, not byte-exact replay)

### Requirement: Exclude clio's own MCP traffic

The system SHALL skip events that are clio's own MCP tool calls to prevent self-pollution.

#### Scenario: Indexing a session that queried clio

- **WHEN** ingest encounters a `tool_use` or `tool_result` whose server/tool name belongs to clio
- **THEN** the system SHALL NOT write that message into `messages` or the FTS index

### Requirement: Per-message size cap and atomic per-file ingest

The system SHALL cap FTS content per message and commit each file in a single
transaction, and SHALL abort a commit whose source snapshot can no longer be validated.

#### Scenario: Oversized tool output

- **WHEN** a single message's content exceeds 64KB
- **THEN** the system SHALL truncate the FTS-indexed content while preserving the full
  `raw_json`

#### Scenario: Crash mid-file

- **WHEN** ingest of a file is interrupted before commit
- **THEN** the system SHALL leave `ingest_state` unchanged so the next run re-ingests
  that file from its prior offset

#### Scenario: Source changed or unreadable during commit re-validation

- **WHEN** the source file's size or mtime changed since it was read, OR the file can no
  longer be stat'd (removed or replaced) at commit time
- **THEN** the system SHALL abort the commit without writing, leave `ingest_state`
  unchanged, and let a later pass re-ingest the fresh bytes

### Requirement: Structured activity index from tool calls

At ingest the system SHALL extract structured activity facts from each non-clio
tool_use and store them in a `tool_targets` table keyed by message and session.
It SHALL record a `tool` fact (the tool name, including MCP `mcp__server__tool`)
for every tool_use, plus a domain fact where applicable: `file`
(Edit/Write/Read/NotebookEdit/MultiEdit file path), `command` (Bash), `pattern`
(Grep/Glob), or `url` (WebFetch). Stored values SHALL be redacted, and clio's own
MCP tools (`mcp__clio__*`) SHALL be excluded.

#### Scenario: Edit records a tool fact and a file fact

- **WHEN** a session contains an `Edit` tool_use on `/x/auth.ts`
- **THEN** `tool_targets` SHALL contain a `tool` fact (`Edit`) and a `file` fact
  (`/x/auth.ts`) for that message

#### Scenario: Bash records a command fact

- **WHEN** a session contains a `Bash` tool_use running `go test ./...`
- **THEN** `tool_targets` SHALL contain a `command` fact whose value is the
  redacted command

#### Scenario: clio's own tools are excluded

- **WHEN** a tool_use is `mcp__clio__search`
- **THEN** no `tool_targets` row SHALL be created for it

### Requirement: One-time activity backfill

The system SHALL populate `tool_targets` for already-indexed messages from their
stored (redacted) `raw_json`, without re-reading source files. The backfill SHALL
be idempotent and run automatically when the table is empty.

#### Scenario: Existing history is backfilled

- **WHEN** the activity index is added to a database that already contains
  tool_use messages
- **THEN** activity queries SHALL return facts for those historical messages
  without requiring a full re-ingest

### Requirement: Subagent transcripts are linked to their parent session

At ingest the system SHALL recognize a Claude Code subagent transcript — a source
file with an ancestor directory named `subagents/` (i.e.
`<project>/<parent-session-uuid>/subagents/agent-<agentId>.jsonl`) — and link it to
its parent conversation instead of indexing it as a standalone top-level session.
The parent session uuid SHALL be taken from the transcript's per-line `sessionId`
field, falling back to the parent directory name when that field is absent, and
stored in `sessions.parent_session`. The subagent's own session uuid SHALL remain
the file's `agent-<agentId>` identifier, and its messages SHALL stay attributed to
it.

#### Scenario: A subagent transcript records its parent link

- **WHEN** a file `<proj>/7ad4.../subagents/agent-a134.jsonl` is ingested and its
  lines carry `sessionId` `7ad4...`
- **THEN** the `agent-a134` session's `parent_session` SHALL be `7ad4...`

#### Scenario: A normal session has no parent link

- **WHEN** an ordinary `<uuid>.jsonl` session file (not under `subagents/`) is
  ingested
- **THEN** its `parent_session` SHALL remain empty

#### Scenario: Parent uuid falls back to the directory name

- **WHEN** a subagent transcript under `.../<parent-uuid>/subagents/` has lines with
  no `sessionId` field
- **THEN** its `parent_session` SHALL be the parent directory name `<parent-uuid>`

### Requirement: Subagent type is recorded

At ingest the system SHALL record the subagent's type in `sessions.agent_type`,
taken from the first non-empty `attributionAgent` value in the transcript (for
example `general-purpose`). When no `attributionAgent` value is present the type
SHALL be empty.

#### Scenario: Type captured from attributionAgent

- **WHEN** a subagent transcript contains an `attributionAgent` value
  `general-purpose`
- **THEN** the session's `agent_type` SHALL be `general-purpose`

#### Scenario: Missing type is empty

- **WHEN** a subagent transcript has no `attributionAgent` value
- **THEN** the session's `agent_type` SHALL be empty (a generic subagent)

### Requirement: Existing subagent sessions are backfilled

The system SHALL reconcile subagent transcripts that were already indexed as orphan
top-level sessions, without requiring a full re-index: the migration SHALL clear the
ingest watermark for files under `subagents/` so the next index pass re-ingests them
in place, populating `parent_session` and `agent_type` on the same session rows
without duplicating rows or changing their uuid.

#### Scenario: Orphans become children after upgrade

- **WHEN** the migration is applied to a database containing orphan `agent-<id>`
  sessions and `clio index` is then run
- **THEN** those sessions SHALL gain their `parent_session` and `agent_type` with no
  change to their uuid and no duplicate rows

### Requirement: Ingestion routes through pluggable source adapters

The system SHALL ingest sessions through registered source adapters, each of which owns
its root directories, file ownership, canonical session id, and whole-file parse and
metadata aggregation. The shared ingest machinery — change classification,
byte-offset/fingerprint incremental resume, the message-insert transaction,
`ingest_state`, FTS, and secret redaction — SHALL remain format-agnostic and apply to
every source. Exactly one adapter SHALL own any discovered file, an adapter SHALL only
own paths under its declared roots, and registered roots SHALL be disjoint.

#### Scenario: Claude Code remains the default source

- **WHEN** a `~/.claude/projects/**/<uuid>.jsonl` file is ingested
- **THEN** it SHALL be handled by the `claude-code` adapter and indexed exactly as before

#### Scenario: A file owned by no adapter is not ingested

- **WHEN** a discovered file is not owned by any registered source adapter
- **THEN** it SHALL be skipped (not indexed) and reported as unowned by diagnostics

### Requirement: Codex CLI transcripts are ingested

The system SHALL ingest OpenAI Codex CLI transcripts found under `~/.codex/sessions`
(`rollout-<timestamp>-<uuid>.jsonl`, append-only JSONL). The canonical session id SHALL
be the uuid in the filename and SHALL be verified to equal the `session_meta.payload.id`;
on mismatch the file SHALL be skipped with an error. The project path SHALL be taken
from the transcript's real `cwd`. The conversation SHALL be normalized from the
`response_item` stream: a `message` with role `user` (with `<environment_context>` and
`<*_instructions>` wrapper blocks stripped, dropped when empty) becomes a user message;
`message` role `assistant` becomes an assistant message; `reasoning` with a non-empty
summary becomes a thinking message; `function_call` becomes a tool-use with extracted
activity targets; and `function_call_output` becomes a tool-result. The duplicate
`event_msg` stream, `developer` messages, and non-conversational records SHALL be skipped.

#### Scenario: A Codex session is indexed from its rollout file

- **WHEN** a `~/.codex/sessions/2026/06/19/rollout-2026-06-19T14-19-57-019ede89-...jsonl` file is ingested
- **THEN** a session with `source` `codex`, the filename uuid, and the project path from its `cwd` SHALL be indexed

#### Scenario: The event_msg stream is not double-counted

- **WHEN** a user turn appears in both `response_item/message(role=user)` and `event_msg/user_message`
- **THEN** exactly one user message SHALL be indexed for that turn

#### Scenario: A wrapper-only user record is not a turn

- **WHEN** a `response_item/message(role=user)` contains only an `<environment_context>` wrapper block
- **THEN** it SHALL NOT produce a user message and SHALL NOT count toward `turn_count`

#### Scenario: A filename/metadata id mismatch is rejected

- **WHEN** a Codex rollout file's filename uuid differs from its `session_meta.payload.id`
- **THEN** the file SHALL be skipped with an error and SHALL NOT be indexed

### Requirement: Each session records its source tool

The system SHALL record the originating tool of every session in a `sessions.source`
column. Sessions ingested before this change SHALL be treated as `claude-code`, and a
row SHALL default to `claude-code` when the column is null or empty.

#### Scenario: Existing sessions default to claude-code

- **WHEN** the migration is applied to a database of existing sessions
- **THEN** those sessions SHALL have `source` `claude-code`

#### Scenario: Codex sessions record their source

- **WHEN** a Codex transcript is ingested
- **THEN** its session `source` SHALL be `codex`

### Requirement: Cross-source uuid collisions fail closed

When a session uuid is already owned by a different source, the system SHALL detect the
conflict before writing any message or tool-target rows, SHALL refuse to ingest the
conflicting file with no rows written, and SHALL record the conflict durably in a
`source_conflicts` table that diagnostics reports. The conflict record SHALL be cleared
when that file is later ingested without conflict. A conflict SHALL never silently
overwrite or drop data without a durable record.

#### Scenario: A conflicting file is refused and recorded

- **WHEN** a file's session uuid already belongs to another source
- **THEN** no rows SHALL be written for it and a `source_conflicts` row SHALL record the file, uuid, and the two sources

#### Scenario: A resolved conflict is cleared

- **WHEN** a previously conflicting file is later ingested without a uuid conflict
- **THEN** its `source_conflicts` row SHALL be removed

### Requirement: Activity backfill is source-scoped

The one-time activity backfill SHALL apply only to `claude-code` rows and SHALL skip
rows from other sources. It reparses stored Claude Code raw events to extract tool
activity, which is meaningful only for Claude Code rows; other sources extract their
activity targets at ingest time.

#### Scenario: Backfill skips Codex rows

- **WHEN** the activity backfill runs over a database containing Codex sessions
- **THEN** it SHALL NOT attempt to reparse Codex rows as Claude Code events

### Requirement: Codex tool calls produce activity targets

The system SHALL extract structured activity targets from Codex `function_call` records at
ingest. `exec_command` SHALL produce a `command` target from its `cmd`; `shell` SHALL
produce a `command` target from the script in its `command` argv — the element following
the shell `-c`/`-lc` flag, falling back to the joined argv when no such flag is present;
`view_image` SHALL produce a `file` target from its `path`; and every non-clio Codex tool
call SHALL produce a `tool` target naming the tool. Codex calls to clio's own MCP tools
SHALL produce no targets. Extracted values SHALL be secret-redacted before being
length-capped. Claude Code target extraction SHALL be unchanged.

#### Scenario: exec_command becomes a command target

- **WHEN** a Codex `exec_command` call with `cmd` `"git status --short"` is ingested
- **THEN** a `command` activity target `"git status --short"` SHALL be recorded for that tool-use

#### Scenario: shell command is extracted from the bash -lc argv

- **WHEN** a Codex `shell` call with `command` `["bash","-lc","go test ./..."]` is ingested
- **THEN** a `command` activity target `"go test ./..."` SHALL be recorded, not the wrapper argv

#### Scenario: view_image becomes a file target

- **WHEN** a Codex `view_image` call with `path` `"/repo/diagram.png"` is ingested
- **THEN** a `file` activity target `"/repo/diagram.png"` SHALL be recorded

#### Scenario: clio's own MCP calls produce no Codex targets

- **WHEN** a Codex `function_call` names one of clio's own MCP tools (`mcp__clio__*`)
- **THEN** no activity targets SHALL be recorded for it

#### Scenario: Codex command targets surface in activity queries

- **WHEN** `clio activity --by command --source codex` runs over indexed Codex sessions
- **THEN** the extracted Codex commands SHALL be listed, and SHALL NOT appear under the default `claude-code` source

#### Scenario: A secret in a Codex command is redacted

- **WHEN** a Codex command containing a secret pattern is ingested
- **THEN** the stored `command` target SHALL have the secret redacted

### Requirement: Codex tool-use messages summarize their command

The system SHALL include the executed command or file path in a Codex tool-use message's
summary, so the message content and full-text index show what the tool did rather than only
the tool name. The summarized value SHALL be secret-redacted before truncation. The shared
Claude Code tool-use summary SHALL be unchanged.

#### Scenario: A Codex command appears in the tool-use summary

- **WHEN** a Codex `exec_command` running `"git status --short"` is ingested
- **THEN** the tool-use message content SHALL include `"git status --short"`, not just `"exec_command"`

#### Scenario: The summary redacts secrets before truncation

- **WHEN** a Codex command begins with a secret-bearing prefix longer than the summary cap
- **THEN** the summary SHALL be redacted on the full command before truncation, leaking no partial secret


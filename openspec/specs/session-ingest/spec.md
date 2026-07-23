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
metadata aggregation. The shared ingest machinery — change classification, the
message-insert transaction, `ingest_state`, FTS, cross-source collision handling, and
secret redaction — SHALL remain format-agnostic and apply to every source. Byte-offset/
fingerprint incremental resume SHALL be the default, but an adapter MAY declare that it is
reconstructed by **whole-file replay** instead; for such a source the orchestrator SHALL
parse the file from offset 0 and commit a full re-ingest whenever the file has changed,
using the stored byte offset only to detect change (skip when unchanged), never as a resume
point. Exactly one adapter SHALL own any discovered file, an adapter SHALL only own paths
under its declared roots, and registered roots SHALL be disjoint.

#### Scenario: Claude Code remains the default source

- **WHEN** a `~/.claude/projects/**/<uuid>.jsonl` file is ingested
- **THEN** it SHALL be handled by the `claude-code` adapter and indexed exactly as before,
  using byte-offset incremental resume unchanged

#### Scenario: A file owned by no adapter is not ingested

- **WHEN** a discovered file is not owned by any registered source adapter
- **THEN** it SHALL be skipped (not indexed) and reported as unowned by diagnostics

#### Scenario: A whole-file-replay source re-ingests its whole file on change

- **WHEN** a file owned by a whole-file-replay adapter changes (grows or is rewritten)
- **THEN** the orchestrator SHALL re-read it from offset 0 and commit a full re-ingest (its
  prior rows replaced), rather than resuming from the stored byte offset
- **AND WHEN** that file is unchanged since the last pass
- **THEN** it SHALL be skipped with no reparse

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

### Requirement: Gemini CLI transcripts are discovered under the chats directory

The system SHALL discover Gemini CLI transcripts as `*.jsonl` files under any `chats/`
directory below `~/.gemini/tmp` (`~/.gemini/tmp/<projectId>/chats/session-<timestamp>-<id8>.jsonl`
and nested subagent files `chats/<parentSessionId>/<childSessionId>.jsonl`). The `gemini`
adapter SHALL own exactly those files; a `.jsonl` file with no `chats/` ancestor SHALL NOT be
owned by it. Discovery SHALL reuse the existing recursive `.jsonl` walker unchanged. The
Gemini root SHALL be disjoint from the Claude Code and Codex roots.

#### Scenario: A Gemini main-session file is owned by the gemini adapter

- **WHEN** `~/.gemini/tmp/gemini-sample/chats/session-2026-07-17T14-18-4ac5c3df.jsonl` is discovered
- **THEN** the `gemini` adapter SHALL own it, and the claude-code fallback SHALL NOT

#### Scenario: Old and non-chats layouts own no files

- **WHEN** a Gemini install has no `chats/` directory (a `≤0.1.9` sha256-hash project dir),
  or the discovered file is `logs.json` or `checkpoint-<tag>.json`
- **THEN** the `gemini` adapter SHALL own none of them and they SHALL be silently skipped
  (not an error)

### Requirement: Gemini project path is resolved from projects.json

The system SHALL resolve a Gemini session's project path by mapping the `chats/`-parent
directory name (the projectId) to an absolute path via `~/.gemini/projects.json`, whose
`projects` object maps absolute project paths to projectIds (the mapping is inverted to look
up by projectId). When `projects.json` is absent or contains no entry for the projectId, the
project path SHALL be left empty and the session SHALL still be indexed.

#### Scenario: projectId maps to its project path

- **WHEN** a transcript lives under `~/.gemini/tmp/gemini-sample/chats/` and `projects.json`
  maps `/abs/path/gemini-sample` → `gemini-sample`
- **THEN** the session's project path SHALL be `/abs/path/gemini-sample`

#### Scenario: Missing mapping leaves project path empty

- **WHEN** `projects.json` is absent or has no entry for the projectId
- **THEN** the session SHALL be indexed with an empty project path (unattributed), not skipped

### Requirement: Gemini session identity comes from the metadata line

The system SHALL take a Gemini session's canonical uuid from the `sessionId` field of the
transcript's first (metadata) line, because a main-session filename carries only an 8-char id
fragment. When purging a deleted Gemini file whose metadata line is no longer readable, the
system SHALL resolve the file's uuid from the database (`sessions.source_file`) so its rows
are removed cleanly.

#### Scenario: The full uuid comes from metadata, not the filename

- **WHEN** a main file named `session-<ts>-4ac5c3df.jsonl` has a metadata `sessionId`
  `4ac5c3df-ca64-4c94-9ad8-d6792fcda807`
- **THEN** the indexed session uuid SHALL be the full `4ac5c3df-ca64-4c94-9ad8-d6792fcda807`,
  not the 8-char filename fragment

#### Scenario: A deleted Gemini file still purges

- **WHEN** a tracked Gemini file is deleted (its metadata line unreadable) and purge runs
- **THEN** its uuid SHALL be resolved from `sessions.source_file` and its session/message rows
  SHALL be removed, leaving no orphans

### Requirement: Gemini transcripts are reconstructed by replaying their $set op-log

The system SHALL treat a Gemini transcript as an op-log and reconstruct the final message
list by replaying, in file order, every record shape confirmed by a real transcript
(`gemini-real-sample-with-assistant.jsonl`, 2026-07-20 ship-gate re-review): the first
(metadata) line SHALL seed the session; a `$set` record carrying a `messages` array SHALL
overwrite the reconstructed messages with that array (last writer wins), including when the
array is present, non-null, but empty (`[]`), which SHALL be treated as a legitimate full
clear of the conversation, not an unusable record; a metadata-only `$set` (no `messages`
key) SHALL update session metadata without changing the message list. A bare (non-`$set`,
non-`$rewindTo`) top-level message record SHALL be replayed via **upsert by message id**
into the reconstructed list: a record whose id already appears in the list SHALL replace
that entry at its original position (never moved to the end), and a record whose id has not
been seen SHALL be appended at the end; a bare record with no id SHALL be skipped with a
warning and counted as unparsed, since upsert has nothing to key on. A `$rewindTo` record —
still not observed in any real transcript to date — SHALL NOT be replayed: it SHALL be
skipped with a warning and counted as unparsed (visible to diagnostics), never aborting the
file; replay support for it (including the rewind's inclusive-vs-exclusive boundary) is
built once a real transcript containing one exists. The `gemini` adapter SHALL be a
whole-file-replay source: it SHALL parse from offset 0 and its reconstruction SHALL be
committed as a full re-ingest, so repeated ingests of the same bytes produce identical rows
and a later `$set` or bare-record upsert that edits, clears, or extends the conversation is
reflected without duplicate or stale rows.

#### Scenario: The last $set wins

- **WHEN** a transcript contains a `$set` followed by a later `$set` whose `messages` array
  differs
- **THEN** the indexed messages SHALL be exactly the later `$set`'s array, with no leftover
  or duplicated messages from the earlier one

#### Scenario: An empty $set messages array is a legitimate clear

- **WHEN** a transcript contains a `$set` whose `messages` value is a present, non-null,
  empty array (`[]`)
- **THEN** the reconstructed message list SHALL become empty (a full clear), and the pass
  SHALL NOT abort and SHALL NOT count the record as unparsed

#### Scenario: A bare message record is replayed via upsert-by-id

- **WHEN** a transcript contains a bare top-level message record whose id has not
  previously appeared
- **THEN** it SHALL be appended to the reconstructed message list at the end
- **AND WHEN** a later bare record carries an id that already appears in the reconstructed
  list (e.g. the same assistant turn arriving once with only `thoughts` and again with
  `toolCalls` added)
- **THEN** it SHALL replace that entry AT ITS ORIGINAL POSITION in the list, not be moved to
  the end
- **AND WHEN** a bare record carries no id
- **THEN** it SHALL be skipped with a warning and counted as unparsed, and SHALL NOT abort
  the file

#### Scenario: An unobserved $rewindTo record is skipped and counted, not replayed

- **WHEN** a transcript contains a `$rewindTo` record
- **THEN** it SHALL be skipped with a warning and counted as unparsed, and the
  reconstruction SHALL come from the metadata, `$set`, and bare-record-upsert records alone,
  with the file's other records still ingested

#### Scenario: Re-ingesting unchanged then changed bytes is idempotent

- **WHEN** a Gemini file is ingested, then re-ingested unchanged, then re-ingested after a
  `$set` or a bare-record upsert that edits an earlier message
- **THEN** the unchanged pass SHALL be a no-op, and after the edit the indexed rows SHALL
  equal a from-scratch ingest of the final bytes (no duplicate rows, no stale rows)

### Requirement: An unusable Gemini state record aborts the pass without losing prior state

The system SHALL read Gemini transcript lines through the shared capped line reader (16 MiB
per line). When a `$set` state record cannot be used — it exceeds the line cap or fails to
parse — the file's ingest pass SHALL abort with a warning rather than commit a partial
replay. The same abort applies when a line after the metadata line fails to parse as a JSON
object at all: such a line's shape cannot be inspected, so it cannot be ruled out as having
been a `$set`, and the abort applies regardless of the line's content (e.g. whether or not it
happens to mention `$set`) — this is deliberately NOT a content-sniffing heuristic. In every
abort case: the previously indexed rows for that session SHALL be preserved unchanged, the
failure SHALL be counted as unparsed, and the file's stored watermark SHALL NOT advance past
the unusable record, so diagnostics (the `doctor` lag check) surface the file instead of it
going silently stale. Rationale: a `$set` carries the entire conversation state, so
skip-and-continue (the per-line policy for ordinary records that DO parse as a JSON object
but carry some other observed, non-`$set` shape) would silently discard the whole update;
abort-and-preserve is the only loss-free option whenever a $set can't be ruled out.

#### Scenario: An over-cap $set aborts and preserves the prior index

- **WHEN** a Gemini file whose earlier state is already indexed grows by a `$set` line that
  exceeds the line cap
- **THEN** the pass SHALL abort with a warning, the previously indexed rows SHALL remain
  unchanged, and no partial reconstruction SHALL be committed

#### Scenario: A line that fails to parse as JSON aborts and preserves the prior index

- **WHEN** a Gemini file whose earlier state is already indexed grows by a line that is not
  syntactically valid JSON (so it cannot be identified as a `$set` or any other record shape),
  whether or not that line's bytes happen to contain the substring `$set`
- **THEN** the pass SHALL abort with a warning, the previously indexed rows SHALL remain
  unchanged, and no partial reconstruction SHALL be committed — the same contract as an
  identified-but-unusable `$set`, not the skip-and-continue path used for a line that DID
  parse as a JSON object but carries an unobserved non-`$set` shape

#### Scenario: The aborted file stays visible to diagnostics

- **WHEN** a Gemini file's ingest pass aborts on an unusable `$set` record or an unparsable
  line
- **THEN** the file's stored watermark SHALL NOT advance past that record and the failure
  SHALL count as unparsed, so `doctor` reports the file as lagging rather than green

### Requirement: Gemini messages map to clio roles and strip harness context

The system SHALL map the reconstructed Gemini messages to clio roles, in v1 covering only
the textual conversation: `type: "user"` to a user message and `type: "gemini"` to an
assistant message. A message's text SHALL be extracted from its `content` field in either
of the two shapes confirmed by a real transcript: a `content[]` array of `{text}` blocks
(used by every `$set`-embedded message observed to date), or a bare JSON string (used by
bare `"gemini"`-type records observed in a real transcript, 2026-07-20 ship-gate
re-review); any other shape (an object, a number, etc.), or an absent/empty content value,
SHALL yield no extractable text. `type: "info" | "error" | "warning"` records SHALL be
skipped. A `gemini` message's `thoughts` and `toolCalls` fields SHALL NOT be extracted in
v1 (their shapes are now confirmed by a real transcript, but extraction into
thinking/tool-use messages and activity targets is a separate, larger piece of work left
for a future change) — they remain present, redacted, in the stored `raw_json`. A message
whose content yields no text via either recognized shape SHALL be skipped: for an assistant
(`gemini`) message this SHALL be a warning and SHALL be counted as unparsed, never silently
indexed as an empty message; for a user message this SHALL simply not produce a turn (no
warning, not counted). A user message's `<session_context>` harness wrapper block SHALL be
stripped, and a wrapper-only user message SHALL NOT produce a turn nor supply the session
title. Message content and stored `raw_json` SHALL be secret-redacted through the shared
redaction machinery, and the session `source` SHALL be `gemini`.

#### Scenario: User and assistant turns are indexed with their source

- **WHEN** a Gemini transcript with a real user prompt and a `gemini` reply is ingested
- **THEN** a user message and an assistant message SHALL be indexed under a session whose
  `source` is `gemini`

#### Scenario: A bare gemini-type message's string content is extracted

- **WHEN** a bare `"gemini"`-type record's `content` field is a bare JSON string (not a
  `content[]` array)
- **THEN** that string SHALL be extracted and indexed as the assistant message's text, the
  same as if it had arrived as a single-element `content[]` array

#### Scenario: A session_context-only user record is not a turn

- **WHEN** a `user` message contains only a `<session_context>` wrapper block (as in the
  live v0.51.0 sample)
- **THEN** it SHALL NOT produce a user message, SHALL NOT count toward `turn_count`, and
  SHALL NOT be used as the session title

#### Scenario: thoughts and toolCalls are not extracted in v1

- **WHEN** a `gemini` message carries `thoughts` or `toolCalls` fields
- **THEN** no thinking or tool-use messages and no activity targets SHALL be produced from
  them in v1, and the fields SHALL remain (redacted) in the message's stored `raw_json`

#### Scenario: An assistant message with an unrecognized content shape is not indexed empty

- **WHEN** a `gemini` message's content yields no text via either recognized shape
- **THEN** it SHALL be skipped with a warning and counted as unparsed, and no empty
  assistant message SHALL be indexed

#### Scenario: A secret in Gemini content is redacted

- **WHEN** a Gemini message contains a secret pattern
- **THEN** the stored content and `raw_json` SHALL have the secret redacted by the shared
  redaction machinery

### Requirement: Nested Gemini transcripts are indexed as flat sessions

The system SHALL index a Gemini transcript nested one level below a `chats/` directory
(`chats/<parentSessionId>/<childSessionId>.jsonl`) as its own session — the discovery
ownership rule already covers it, its uuid comes from its own metadata `sessionId`, and its
content is searchable like any session. In v1 no `parent_session` link SHALL be recorded
(the nested-child layout and any agent-type metadata are unobserved; parent linking is built
by this change's real-sample re-confirmation task). A top-level main file (metadata
`kind: "main"`) SHALL have no parent link.

#### Scenario: A nested Gemini transcript is indexed flat

- **WHEN** `chats/<parent-uuid>/<child-uuid>.jsonl` is ingested
- **THEN** it SHALL be indexed as a session with uuid `<child-uuid>` (from its metadata),
  searchable like any session, with no `parent_session` link in v1

#### Scenario: A top-level Gemini session has no parent

- **WHEN** a `chats/session-<ts>-<id8>.jsonl` main file (metadata `kind: "main"`) is ingested
- **THEN** its session SHALL have no `parent_session`

### Requirement: Gemini appears across source surfaces via the registry

The system SHALL expose `gemini` through the source registry as a single seed entry, so that
`--source gemini`, the MCP read tools' `source` enum and parameter, the TUI source label
`[gemini]`, and `doctor`'s per-source root report all include Gemini without editing those
surfaces individually.

#### Scenario: The single registry entry wires every surface

- **WHEN** the `gemini` entry is present in the registry seed
- **THEN** `--source gemini` SHALL be accepted, the MCP `source` enum SHALL include `gemini`,
  the TUI SHALL label Gemini rows `[gemini]`, and `doctor` SHALL report the Gemini chats dir —
  with no per-surface source list edited

### Requirement: Parsers extract token usage per source
Ingest SHALL extract token usage during normal indexing for all three sources, each producing a
deterministic aggregate from complete state: Claude Code runs a dedicated usage pass over the
session's **original source file** (extracting only outer uuid, `message.model`, and
`message.usage`; deduplicated by uuid with the later line winning; grouped by model) — the
filtered `messages` table MUST NOT be used as the usage source, because the parser excludes
clio-MCP tool-use events that still carry usage; Codex takes the latest cumulative
`total_token_usage` observed in the session's rollout file (one rollout file is one session —
no cross-file summing) and always writes the `(mixed)` model sentinel; Gemini sums per-message
`tokens` during whole-file replay. The Claude usage scan SHALL run outside the write
transaction, SHALL cover exactly the committed complete-line watermark `[0, newOffset)` with
the same complete-line and line-cap semantics as text ingest, and the aggregate replacement
SHALL execute inside the commit transaction after message inserts and before commit — on the
main commit path only: the cross-source-conflict early-exit path SHALL be a usage no-op. A
usage scan that fails mid-pass SHALL be an atomic no-op: no replace, no delete, existing rows
retained. Codex `rate_limits` payloads SHALL be captured into quota
snapshots with the event timestamp as `observed_at` under the timestamp-guarded upsert.

#### Scenario: Claude duplicate uuids count once
- **WHEN** a Claude session file contains the same message uuid on multiple JSONL lines
- **THEN** the session's usage aggregate counts that uuid's usage exactly once, using the later
  line's values

#### Scenario: Claude usage on parser-excluded events still counted
- **WHEN** a Claude session contains an assistant event whose only content block is a clio MCP
  tool call (which produces zero message rows in the text index)
- **THEN** that event's `message.usage` is still included in the session's aggregate

#### Scenario: Claude incremental tail yields full-session totals
- **WHEN** a pre-existing indexed Claude session receives new messages via incremental ingest
- **THEN** the recomputed aggregate covers the entire source file, not only the new tail

#### Scenario: Codex cumulative counter not double-summed
- **WHEN** a Codex rollout contains successive `token_count` events with growing cumulative
  totals
- **THEN** the aggregate equals the latest cumulative value, not the sum of all events

#### Scenario: Codex tail without token events is a no-op
- **WHEN** an incremental read of a Codex rollout tail contains no `token_count` events
- **THEN** the session's existing usage row is left untouched

#### Scenario: Full rebuild without usage deletes stale row
- **WHEN** a full rebuild of a session file finds no usage events but a stale usage row exists
- **THEN** the stale row is deleted

#### Scenario: Gemini replay idempotent
- **WHEN** the same Gemini session is re-indexed twice via whole-file replay
- **THEN** the usage aggregate is identical after both runs

#### Scenario: Cross-source conflict leaves usage untouched
- **WHEN** a file from a different source carrying usage is rejected as a session-UUID
  conflict (the early-exit commit path)
- **THEN** the existing session's `session_usage` rows are completely unchanged

#### Scenario: Uncommitted tail line not counted
- **WHEN** a Claude session file ends in an incomplete line beyond the committed watermark
- **THEN** the usage scan does not count it; it is picked up once text ingest commits it

#### Scenario: Failed scan is an atomic no-op flagged stale
- **WHEN** the usage pass encounters a hard mid-scan read error during a full rebuild
- **THEN** the session's previous usage rows are retained — neither partially replaced nor
  deleted — and the file's `usage_stale` flag is set until a successful rescan

#### Scenario: Oversized line skipped, scan continues
- **WHEN** a Claude session file contains a historical line exceeding the line cap
- **THEN** the usage scan skips it, counts it in `usage_skipped`, and later appends to the
  same file still update the usage aggregate

### Requirement: Full reindex backfills usage
`clio index --full` SHALL rebuild `session_usage` for all existing sessions. Incremental ingest
SHALL keep aggregates current for sessions it touches. When `--full` is requested while the MCP
server holds the index lock, the command SHALL exit non-zero with a message naming the lock
holder; it MUST NOT report success without doing the work.

#### Scenario: Backfill on full reindex
- **WHEN** a user upgrades and runs `clio index --full`
- **THEN** sessions indexed before the upgrade have usage aggregates

#### Scenario: Locked full reindex refuses loudly
- **WHEN** `clio index --full` runs while the MCP server holds the index lock
- **THEN** the command exits non-zero and instructs the user to stop the MCP server, and no
  "success/nothing to do" is reported

### Requirement: Usage diagnostics persist per file
`ingest_state` SHALL gain `usage_skipped` and `usage_unmapped` counters whose update semantics
follow the scope of the extraction pass: whole-file passes (Claude usage scan, Gemini replay,
any full ingest) SHALL replace the counters with that pass's result; only tail-scoped
extraction (Codex incremental) accumulates. Surfaced by `clio doctor` alongside
`unparsed_lines`.

#### Scenario: Whole-file pass replaces counters
- **WHEN** a Claude file containing one malformed usage line receives three successive
  incremental appends (each triggering a whole-file usage scan)
- **THEN** `usage_skipped` remains 1, not 3

#### Scenario: Full re-ingest replaces counters
- **WHEN** a file with malformed usage events is fully re-ingested
- **THEN** its `usage_skipped` value reflects that run alone (replace, not accumulate)

### Requirement: Bounded storage growth and ingest cost
The usage tables SHALL add O(session-model pairs) rows only (bounded by a small constant
factor over sessions in practice). All gates below are measured under the
protocol in design.md (versioned fixture set, fresh DB, `wal_checkpoint(TRUNCATE)` before size
reads, main-DB size as page_count × page_size; two timing layers per design.md — end-to-end
CLI metrics via interleaved fresh-sandbox CLI invocations n≥10, component metrics via
order-counterbalanced Go benchmarks — comparing medians; absolute bounds are defined for the reference hardware class recorded in
perf/measurements.md and MUST be recalibrated proportionally to the measured baseline
in-process tail time on materially different hardware). On the reference fixture set:
post-checkpoint DB growth MUST be < 2%; full-index
wall-clock regression MUST be < 5%; on the 5,000-message long-session Claude reference fixture
the usage pass MUST add < 20 ms absolute to a single in-process tail ingest (the CLI-level
tail delta is recorded as informational — it proved an unstable metric, ~11-15 ms of real cost
plus machine noise over a denominator of mostly fixed command overhead), with write-lock hold
time increase < 10% one-sided measured in alternated pairs (the ~2× fixture
is an informational slope point, not a gate — the scan is O(file) per touch by design, and the
documented escalation path for longer sessions is a per-file usage cursor as its own change;
an in-process RELATIVE bound is unmeetable by construction over a ~3 ms baseline, see design);
Gemini single-session re-replay combined DB+WAL file-size growth MUST be < 10%; and the
doctor DB/source ratio MUST stay below the existing 3× warning threshold.

#### Scenario: Growth and throughput measured against numeric gates
- **WHEN** the reference fixture set is indexed with and without usage extraction under the
  measurement protocol
- **THEN** the recorded deltas are documented in the change dir and each is within its numeric
  gate above

#### Scenario: Long-session tail ingest stays within its gate
- **WHEN** a single incremental tail is ingested into the long-session Claude fixture with the
  usage pass enabled
- **THEN** the in-process absolute overhead is < 20 ms and write-lock hold time increase is
  < 10% (one-sided, alternated pairs), with the CLI-level delta and scanned bytes/rows recorded


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

### Requirement: Parsers extract token usage per source
Ingest SHALL extract token usage during normal indexing for both sources, each producing a
deterministic aggregate from complete state: Claude Code runs a dedicated usage pass over the
session's **original source file** (extracting only outer uuid, `message.model`, and
`message.usage`; deduplicated by uuid with the later line winning; grouped by model) — the
filtered `messages` table MUST NOT be used as the usage source, because the parser excludes
clio-MCP tool-use events that still carry usage; Codex takes the latest cumulative
`total_token_usage` observed in the session's rollout file (one rollout file is one session —
no cross-file summing) and always writes the `(mixed)` model sentinel. The Claude usage scan SHALL run outside the write
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
follow the scope of the extraction pass: whole-file passes (Claude usage scan,
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
an in-process RELATIVE bound is unmeetable by construction over a ~3 ms baseline, see design); and the
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

### Requirement: User-marked private content is not indexed

The ingester SHALL remove every `<private>…</private>` element, including its
content, from user and assistant text before persisting, so that the text appears in
neither the searchable `content`, the stored `raw_json`, nor the derived session
title. Matching SHALL be case-insensitive on the tag name, SHALL tolerate attributes
and surrounding whitespace, SHALL treat nested elements as one span (an inner `<private>` never ends the
outer element early; two sibling blocks in one message remove exactly two spans), and
SHALL leave an unterminated `<private>` opening tag's text in
place (no silent loss of the rest of the message), and SHALL NOT treat a self-closing
`<private/>` as an opener. Removal SHALL apply to the same
text fields the secret redaction already walks, SHALL happen before redaction and before
any JSON-aware splitting of the text (a private block whose content is a JSON object or
array is removed whole), SHALL be block-local (an element that opens in one top-level content
block and closes in another is not recognised; the joined text of a tool_result's nested
blocks counts as one block for `content`, while `raw_json` keeps the halves), SHALL apply
to every source clio ingests (Claude Code, Codex), and SHALL never modify the
source JSONL. Tool inputs and tool results SHALL be walked the
same way when they are strings or string leaves.

#### Scenario: Private block in a user prompt

- **WHEN** a user message contains `plan the migration <private>the customer is
  Acme, contract value 2M</private> by Friday`
- **THEN** the stored `content` and `raw_json` SHALL read `plan the migration  by
  Friday` (the element and its content gone) and a search for `Acme` SHALL not find
  the message

#### Scenario: Private block containing JSON

- **WHEN** a message contains `<private>{"customer":"Acme"}</private>`
- **THEN** the whole element SHALL be removed from `content` and `raw_json`, and `Acme`
  SHALL not be searchable

#### Scenario: Block spanning two content blocks

- **WHEN** `<private>` opens in one text block of an event and `</private>` sits in the
  next block
- **THEN** both blocks SHALL be stored unchanged apart from normal redaction (documented
  non-support)

#### Scenario: Block spanning a tool_result's nested blocks

- **WHEN** a tool_result's `content` array holds `head <private>alpha` and
  `beta</private> tail` as two text blocks
- **THEN** the stored `content` (the joined text) SHALL have the element removed and
  keep `head`/`tail`, and `raw_json` SHALL keep both halves (documented asymmetry of
  the unsupported form)

#### Scenario: Message that is only a private block

- **WHEN** a user message, or a tool input value, is nothing but
  `<private>secret</private>`
- **THEN** no message row (and no activity fact) SHALL be stored for it, so it never
  counts as a turn

#### Scenario: Applies to other sources

- **WHEN** a Codex transcript message contains `<private>sample</private>`
- **THEN** the stored `content` SHALL not contain `sample`

#### Scenario: Nested private blocks

- **WHEN** a message contains `<private>alpha <private>beta</private> gamma</private>`
- **THEN** the whole outer element SHALL be removed and neither `alpha`, `beta` nor
  `gamma` SHALL be stored

#### Scenario: Two blocks in one message

- **WHEN** a message contains two separate `<private>` elements
- **THEN** exactly those two spans SHALL be removed and the text between them kept

#### Scenario: Unterminated tag

- **WHEN** a message contains `<private>` with no closing tag
- **THEN** the message SHALL be stored unchanged apart from normal redaction

#### Scenario: Stray opener before a complete element

- **WHEN** a message contains a lone `<private>` (no closer) and, later in the same
  message, a complete `<private>secret</private>`
- **THEN** the lone opener SHALL stay as literal text and the complete element SHALL
  still be removed, so `secret` is never stored

#### Scenario: Self-closing tag

- **WHEN** a message contains `x <private/> y <private>s</private> z`
- **THEN** only the second element SHALL be removed and `y` SHALL be kept

#### Scenario: Long private block in a tool input

- **WHEN** a tool call's `command` contains a `<private>` block longer than the
  200-byte summary cut
- **THEN** the stored tool summary SHALL contain none of the block's content

#### Scenario: Title never contains private content

- **WHEN** the first user message of a session opens with a `<private>` block
  followed by real text
- **THEN** the derived `sessions.title` SHALL come from the real text only

#### Scenario: Previously indexed sessions

- **WHEN** a session was indexed by a clio version without this rule
- **THEN** its stored text is unchanged until `clio index --full` re-ingests it; the
  README SHALL state this and that Claude Code's own transcript file is never edited

### Requirement: The set of source names comes from a source registry

The system SHALL maintain a source registry — a compile-time static-seeded list of the
registered sources' names, display labels, and root availability — that is the single
source of truth for the set of source names. Surfaces that enumerate or branch on sources —
CLI `--source` validation and help, the CLI bootstrap source-availability check, the MCP
read tools' `source` enum and defaults, the `doctor` per-source report, the TUI source
labels, and the database source-filter default — SHALL derive their values from this
registry rather than from hardcoded literals or per-source-name branches. With only
`claude-code` and `codex` seeded, every derived value SHALL be identical to the previously
hardcoded `{claude-code, codex, all}` values and the `[codex]` label, so behavior is
unchanged. Ingest semantics, incremental behavior, the `Source` interface, and the schema
SHALL be unchanged by this requirement.

#### Scenario: A source added to the seed appears without editing each surface

- **WHEN** a new source entry is added to the registry seed
- **THEN** its name SHALL become a valid `--source` and MCP `source` value and SHALL be
  labeled in the TUI and reported by `doctor`, without editing the CLI, MCP, TUI, or
  doctor source lists individually

#### Scenario: The default and enum are unchanged for the current two sources

- **WHEN** only `claude-code` and `codex` are seeded
- **THEN** the derived `--source`/MCP enum SHALL be exactly `claude-code`, `codex`, `all`
  with default `claude-code`, and the TUI SHALL label codex rows `[codex]` as before

#### Scenario: The registry is static within a process

- **WHEN** the MCP server has advertised its tools' `source` enum
- **THEN** the set of accepted source names SHALL NOT change for the life of the process,
  because the registry is seeded at compile time rather than registered at runtime

### Requirement: Indexed history outlives its source transcript

Claude Code deletes transcripts older than `cleanupPeriodDays` (default 30). The system SHALL
NOT delete indexed rows merely because a source file disappeared. Reconciliation (catch-up,
MCP startup, watcher backstop) SHALL instead set `ingest_state.source_missing_at` to the
current time for a tracked file confirmed gone (a `not-exist` stat result under an available
source root), leaving its `sessions`, `messages` (and FTS), `tool_calls`, `tool_targets`,
`session_usage` and `ingest_state` rows in place; an existing mark SHALL NOT be overwritten.
A marked file that exists again SHALL be unmarked by the next reconciliation pass, and any
successful ingest commit SHALL clear the mark. When the session row for a vanished file's
uuid now belongs to a different path (the file was moved and re-ingested there), the stale
`ingest_state` row SHALL be dropped instead of marked. Marking is reversible and SHALL NOT be
subject to the mass-disappearance cap that guards deletion. A full re-index
(`clio index --full`) SHALL leave retained sessions and all their rows intact.

#### Scenario: Deleted transcript is retained and marked

- **WHEN** a tracked transcript is deleted and reconciliation runs
- **THEN** its session and messages SHALL remain searchable and its `ingest_state` row SHALL
  carry a non-NULL `source_missing_at`

#### Scenario: Reappearing file is unmarked

- **WHEN** a marked file exists again (with unchanged bytes, so ingest skips it)
- **THEN** the next reconciliation pass SHALL set `source_missing_at` back to NULL

#### Scenario: Successful ingest clears the mark

- **WHEN** a marked file is ingested successfully
- **THEN** its `source_missing_at` SHALL be NULL

#### Scenario: Full re-index keeps retained sessions

- **WHEN** `clio index --full` runs while a session is retained
- **THEN** that session's `sessions`, `messages`, `tool_targets`, `session_usage` and
  `ingest_state` rows SHALL be unchanged and its mark SHALL remain

#### Scenario: Mass disappearance is marked, not deleted

- **WHEN** most tracked files of a large corpus vanish at once
- **THEN** every vanished file SHALL be marked and no rows SHALL be deleted

### Requirement: JSON object keys are redacted like string values

When redaction walks structured JSON, each object key SHALL pass through the same string
redaction as a string value (secret shape patterns and `<private>…</private>` removal), so
a token or a private element used as a key never reaches `content` or `raw_json`.
Whether a key names a secret (and so redacts its whole value) SHALL still be judged on the
original key. When two keys redact to the same name, the entry SHALL collapse to a single
key holding `[REDACTED:key]`, so neither value is kept under a name it never had.

#### Scenario: Token used as a key

- **WHEN** an event's JSON contains `{"ghp_…36 chars…":1}`
- **THEN** the stored form SHALL carry the redaction marker in place of the key and no
  part of the token

#### Scenario: Private element used as a key

- **WHEN** an event's JSON contains `{"<private>x</private>":"v"}`
- **THEN** the element SHALL be removed from the stored key and `x` SHALL not be searchable

#### Scenario: Ordinary keys are unchanged

- **WHEN** an event's JSON contains `{"name":"v"}`
- **THEN** the key SHALL be stored as `name`

#### Scenario: Keys that redact to the same name

- **WHEN** two different token keys in one object both redact to the same marker
- **THEN** the object SHALL hold one entry under that marker whose value is `[REDACTED:key]`


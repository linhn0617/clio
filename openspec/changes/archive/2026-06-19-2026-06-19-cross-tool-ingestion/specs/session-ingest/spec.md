## ADDED Requirements

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

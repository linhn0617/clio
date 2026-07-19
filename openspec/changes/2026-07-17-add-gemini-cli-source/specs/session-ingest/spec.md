## MODIFIED Requirements

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

## ADDED Requirements

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
list by replaying, in file order, the record shapes that have been observed in a real
transcript: the first (metadata) line SHALL seed the session; a `$set` record carrying a
`messages` array SHALL overwrite the reconstructed messages with that array (last writer
wins); a metadata-only `$set` (no `messages` key) SHALL update session metadata without
changing the message list. Record shapes that have NOT been observed in a real transcript —
a bare `MessageRecord` append and a `$rewindTo` rewind — SHALL NOT be replayed in v1: such a
record SHALL be skipped with a warning and counted as unparsed (visible to diagnostics),
never aborting the file; replay support for them (including the rewind's
inclusive-vs-exclusive boundary) is built by this change's real-sample re-confirmation task
once real bytes exist. The `gemini` adapter SHALL be a whole-file-replay source: it SHALL
parse from offset 0 and its reconstruction SHALL be committed as a full re-ingest, so
repeated ingests of the same bytes produce identical rows and a later `$set` that edits or
shrinks the conversation is reflected without duplicate or stale rows.

#### Scenario: The last $set wins

- **WHEN** a transcript contains a `$set` followed by a later `$set` whose `messages` array
  differs
- **THEN** the indexed messages SHALL be exactly the later `$set`'s array, with no leftover
  or duplicated messages from the earlier one

#### Scenario: Unobserved record shapes are skipped and counted, not replayed

- **WHEN** a transcript contains a bare `MessageRecord` or a `$rewindTo` record
- **THEN** that record SHALL be skipped with a warning and counted as unparsed, and the
  reconstruction SHALL come from the metadata and `$set` records alone, with the file's other
  records still ingested

#### Scenario: Re-ingesting unchanged then changed bytes is idempotent

- **WHEN** a Gemini file is ingested, then re-ingested unchanged, then re-ingested after a
  `$set` that edits an earlier message
- **THEN** the unchanged pass SHALL be a no-op, and after the edit the indexed rows SHALL equal
  a from-scratch ingest of the final bytes (no duplicate rows, no stale rows)

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
assistant message, each from the joined `content[].text`; `type: "info" | "error" |
"warning"` records SHALL be skipped. A `gemini` message's `thoughts` and `toolCalls` fields
SHALL NOT be extracted in v1 (their shapes are unobserved; extraction into
thinking/tool-use messages and activity targets is built by this change's real-sample
re-confirmation task) — they remain present, redacted, in the stored `raw_json`. A `gemini`
message whose content yields no text via `content[].text` SHALL be skipped with a warning
and counted as unparsed, never silently indexed as an empty message. A user message's
`<session_context>` harness wrapper block SHALL be stripped, and a wrapper-only user message
SHALL NOT produce a turn nor supply the session title. Message content and stored `raw_json`
SHALL be secret-redacted through the shared redaction machinery, and the session `source`
SHALL be `gemini`.

#### Scenario: User and assistant turns are indexed with their source

- **WHEN** a Gemini transcript with a real user prompt and a `gemini` reply is ingested
- **THEN** a user message and an assistant message SHALL be indexed under a session whose
  `source` is `gemini`

#### Scenario: A session_context-only user record is not a turn

- **WHEN** a `user` message contains only a `<session_context>` wrapper block (as in the live
  v0.51.0 sample)
- **THEN** it SHALL NOT produce a user message, SHALL NOT count toward `turn_count`, and SHALL
  NOT be used as the session title

#### Scenario: thoughts and toolCalls are not extracted in v1

- **WHEN** a `gemini` message carries `thoughts` or `toolCalls` fields
- **THEN** no thinking or tool-use messages and no activity targets SHALL be produced from
  them in v1, and the fields SHALL remain (redacted) in the message's stored `raw_json`

#### Scenario: An assistant message with an unrecognized content shape is not indexed empty

- **WHEN** a `gemini` message's content yields no text via `content[].text`
- **THEN** it SHALL be skipped with a warning and counted as unparsed, and no empty assistant
  message SHALL be indexed

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

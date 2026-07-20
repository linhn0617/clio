## MODIFIED Requirements

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

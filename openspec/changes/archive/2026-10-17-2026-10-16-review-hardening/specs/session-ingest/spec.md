## ADDED Requirements

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

## ADDED Requirements

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

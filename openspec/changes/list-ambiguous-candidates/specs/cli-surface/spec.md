## ADDED Requirements

### Requirement: Ambiguous session id lists candidates

The system SHALL, when a session-id prefix matches more than one session and none
exactly, report the matching sessions so the user can retry with a longer id.

#### Scenario: Candidates are listed

- **WHEN** the user runs `clio show <prefix>` (or calls MCP `read_session`) with a
  prefix that matches several sessions and is not itself a session id
- **THEN** the error SHALL list at most 10 matching sessions, top-level sessions
  before subagents (marked as subagents), each group newest first, each with its
  start time and a single-line title without terminal escapes

#### Scenario: Listed ids resolve

- **WHEN** the user retries with an id shown in the list
- **THEN** it SHALL resolve to that session: the shown id is the shortest prefix
  (at least 8 characters, or the whole id when it is shorter) that matches no other
  session in scope and is no other session's exact id

#### Scenario: More matches than listed

- **WHEN** more than 10 sessions match the prefix
- **THEN** the error SHALL list 10 of them and state that more sessions match

#### Scenario: Empty id

- **WHEN** the session id argument is empty
- **THEN** the system SHALL report an empty id rather than list any sessions

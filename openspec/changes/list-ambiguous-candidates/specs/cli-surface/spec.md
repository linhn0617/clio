## ADDED Requirements

### Requirement: Ambiguous session id lists candidates

The system SHALL, when a session-id prefix matches more than one session and none
exactly, report the matching sessions so the user can retry with a longer id.

#### Scenario: Candidates are listed

- **WHEN** the user runs `clio show <prefix>` (or calls MCP `read_session`) with a
  prefix that matches several sessions and is not itself a session id
- **THEN** the error SHALL list at most 10 matching sessions, newest first, each with
  an id prefix that distinguishes it from the other listed candidates (at least 8
  characters, or the whole id when it is shorter), its start time and a single-line title without terminal escapes

#### Scenario: More matches than listed

- **WHEN** more than 10 sessions match the prefix
- **THEN** the error SHALL list 10 of them and state that more sessions match

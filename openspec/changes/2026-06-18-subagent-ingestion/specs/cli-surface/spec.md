## ADDED Requirements

### Requirement: Subagent sessions are nested under their parent in the CLI

`clio list` SHALL list only top-level sessions by default, excluding subagent child
sessions (those with a populated `parent_session` whose parent session exists), and
SHALL annotate a parent that spawned subagents with a `(+N subagents)` marker. A
`--include-subagents` flag SHALL include subagent sessions in the listing. A
subagent whose parent session is not indexed SHALL still be listed (orphan
promotion), so nothing becomes invisible.

#### Scenario: Subagents are hidden from the default list

- **WHEN** the user runs `clio list` and a parent session spawned 3 subagents
- **THEN** the 3 subagent sessions SHALL NOT appear as separate rows, and the parent
  SHALL be annotated `(+3 subagents)`

#### Scenario: Subagents can be included

- **WHEN** the user runs `clio list --include-subagents`
- **THEN** subagent sessions SHALL appear in the listing

#### Scenario: An orphan subagent stays visible

- **WHEN** a subagent session's parent is not indexed
- **THEN** that subagent SHALL still be listed at top level

### Requirement: show surfaces a session's subagents

`clio show <parent>` SHALL list the parent's subagents (id, type, title) as a
drillable trailing section, and SHALL inline their transcripts when
`--include-subagents` is given. `clio show <agent-id>` SHALL render the subagent
transcript with a header identifying it as a subagent of its parent and its type.

#### Scenario: A parent lists its subagents

- **WHEN** the user runs `clio show <parent>` for a session that spawned subagents
- **THEN** the output SHALL list each subagent's id, type, and title

#### Scenario: A subagent shows its parent in a header

- **WHEN** the user runs `clio show <agent-id>`
- **THEN** the output SHALL include a header noting it is a subagent (with its type)
  of its parent session

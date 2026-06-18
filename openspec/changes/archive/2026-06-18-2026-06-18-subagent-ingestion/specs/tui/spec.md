## ADDED Requirements

### Requirement: The TUI nests subagents under their parent

The TUI Browse view SHALL list only top-level sessions by default and SHALL allow
expanding a parent to reveal its subagents as indented child rows (and collapsing
them again), with the parent row showing its subagent count. The Search view SHALL
mark a hit that comes from a subagent transcript (for example with a `↳` indicator
and the subagent's type). Selecting a parent MAY inline its subagents in the
preview pane.

#### Scenario: Expand a parent to see its subagents

- **WHEN** the user expands a Browse row for a session that spawned subagents
- **THEN** the subagents SHALL appear as indented child rows beneath it

#### Scenario: Collapse hides the subagents again

- **WHEN** the user collapses an expanded parent row
- **THEN** its subagent rows SHALL no longer be shown

#### Scenario: Search marks a subagent hit

- **WHEN** a Search hit comes from a subagent transcript
- **THEN** the result row SHALL be marked as a subagent (with its type)

## MODIFIED Requirements

### Requirement: The TUI is source-aware

The TUI SHALL label each session or result row with its source when sources other than
Claude Code are shown, taking the label from the **source registry** rather than a
hardcoded per-source string, and SHALL accept a launch-time `--source` filter whose
accepted values are the registered source names plus `all` (derived from the registry),
defaulting to `claude-code`.

#### Scenario: Default TUI shows only Claude Code

- **WHEN** the user launches `clio tui` with Codex sessions indexed
- **THEN** Browse and Search SHALL show only `claude-code` sessions

#### Scenario: Source filter at launch

- **WHEN** the user launches `clio tui --source all`
- **THEN** rows from both sources SHALL appear, each labeled with its source

#### Scenario: Row labels come from the registry

- **WHEN** a row from a non-Claude-Code source is rendered
- **THEN** its label SHALL be the registry label for that source (for `codex`, `[codex]` as
  before), not a value hardcoded in the view

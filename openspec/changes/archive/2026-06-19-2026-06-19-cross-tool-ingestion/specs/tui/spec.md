## ADDED Requirements

### Requirement: The TUI is source-aware

The TUI SHALL label each session or result row with its source when sources other than
Claude Code are shown, and SHALL accept a launch-time `--source` filter (`claude-code`,
`codex`, or `all`) defaulting to `claude-code`.

#### Scenario: Default TUI shows only Claude Code

- **WHEN** the user launches `clio tui` with Codex sessions indexed
- **THEN** Browse and Search SHALL show only `claude-code` sessions

#### Scenario: Source filter at launch

- **WHEN** the user launches `clio tui --source all`
- **THEN** rows from both sources SHALL appear, each labeled with its source

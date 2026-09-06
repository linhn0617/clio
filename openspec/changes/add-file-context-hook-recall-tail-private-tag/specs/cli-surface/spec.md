## MODIFIED Requirements

### Requirement: CLI subcommands

The system SHALL expose `index`, `search`, `list`, `show`, `mcp`, `doctor`, `install-mcp`, `uninstall-mcp`, and `file-history` subcommands via a single `clio` binary.

#### Scenario: Help lists subcommands

- **WHEN** the user runs `clio --help`
- **THEN** the system SHALL list all subcommands with short descriptions

### Requirement: Recall and hook commands

The CLI SHALL provide `clio recall` (print the current project's recall digest,
with `--tail-runes` bounding the "last session left off" excerpt),
`clio file-history <path>` (print one file's past-session timeline; with `--hook`
it reads a PreToolUse payload from stdin instead and answers in hook JSON),
`clio install-hook` (register the SessionStart recall hook and, unless
`--no-file-context`, the PreToolUse file-history hook), and
`clio uninstall-hook` (remove both).

#### Scenario: recall prints the digest

- **WHEN** the user runs `clio recall` in an indexed project
- **THEN** the last-session excerpt (when present) followed by the recent
  sessions / files / commands digest SHALL be printed to stdout

#### Scenario: file-history prints a timeline

- **WHEN** the user runs `clio file-history <path>` for a file with indexed history
- **THEN** the sessions that touched it SHALL be printed newest first

#### Scenario: install-hook is independent of MCP registration

- **WHEN** the user runs `clio install-hook`
- **THEN** the hooks SHALL be registered without changing the
  `~/.claude.json` MCP-server registration

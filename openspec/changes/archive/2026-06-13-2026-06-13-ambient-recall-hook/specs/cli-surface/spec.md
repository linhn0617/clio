## ADDED Requirements

### Requirement: Recall and hook commands

The CLI SHALL provide `clio recall` (print the current project's recall digest),
`clio install-hook` (register the SessionStart recall hook), and
`clio uninstall-hook` (remove it).

#### Scenario: recall prints the digest

- **WHEN** the user runs `clio recall` in an indexed project
- **THEN** the recent sessions / files / commands digest SHALL be printed to stdout

#### Scenario: install-hook is independent of MCP registration

- **WHEN** the user runs `clio install-hook`
- **THEN** the SessionStart hook SHALL be registered without changing the
  `~/.claude.json` MCP-server registration

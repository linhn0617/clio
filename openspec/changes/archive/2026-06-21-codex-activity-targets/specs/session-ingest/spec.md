## ADDED Requirements

### Requirement: Codex tool calls produce activity targets

The system SHALL extract structured activity targets from Codex `function_call` records at
ingest. `exec_command` SHALL produce a `command` target from its `cmd`; `shell` SHALL
produce a `command` target from the script in its `command` argv — the element following
the shell `-c`/`-lc` flag, falling back to the joined argv when no such flag is present;
`view_image` SHALL produce a `file` target from its `path`; and every non-clio Codex tool
call SHALL produce a `tool` target naming the tool. Codex calls to clio's own MCP tools
SHALL produce no targets. Extracted values SHALL be secret-redacted before being
length-capped. Claude Code target extraction SHALL be unchanged.

#### Scenario: exec_command becomes a command target

- **WHEN** a Codex `exec_command` call with `cmd` `"git status --short"` is ingested
- **THEN** a `command` activity target `"git status --short"` SHALL be recorded for that tool-use

#### Scenario: shell command is extracted from the bash -lc argv

- **WHEN** a Codex `shell` call with `command` `["bash","-lc","go test ./..."]` is ingested
- **THEN** a `command` activity target `"go test ./..."` SHALL be recorded, not the wrapper argv

#### Scenario: view_image becomes a file target

- **WHEN** a Codex `view_image` call with `path` `"/repo/diagram.png"` is ingested
- **THEN** a `file` activity target `"/repo/diagram.png"` SHALL be recorded

#### Scenario: clio's own MCP calls produce no Codex targets

- **WHEN** a Codex `function_call` names one of clio's own MCP tools (`mcp__clio__*`)
- **THEN** no activity targets SHALL be recorded for it

#### Scenario: Codex command targets surface in activity queries

- **WHEN** `clio activity --by command --source codex` runs over indexed Codex sessions
- **THEN** the extracted Codex commands SHALL be listed, and SHALL NOT appear under the default `claude-code` source

#### Scenario: A secret in a Codex command is redacted

- **WHEN** a Codex command containing a secret pattern is ingested
- **THEN** the stored `command` target SHALL have the secret redacted

### Requirement: Codex tool-use messages summarize their command

The system SHALL include the executed command or file path in a Codex tool-use message's
summary, so the message content and full-text index show what the tool did rather than only
the tool name. The summarized value SHALL be secret-redacted before truncation. The shared
Claude Code tool-use summary SHALL be unchanged.

#### Scenario: A Codex command appears in the tool-use summary

- **WHEN** a Codex `exec_command` running `"git status --short"` is ingested
- **THEN** the tool-use message content SHALL include `"git status --short"`, not just `"exec_command"`

#### Scenario: The summary redacts secrets before truncation

- **WHEN** a Codex command begins with a secret-bearing prefix longer than the summary cap
- **THEN** the summary SHALL be redacted on the full command before truncation, leaking no partial secret

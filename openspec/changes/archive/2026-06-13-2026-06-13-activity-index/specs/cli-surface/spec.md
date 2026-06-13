## ADDED Requirements

### Requirement: Activity filters and summary in the CLI

`clio list` and `clio search` SHALL accept `--touched <path-prefix>`, `--tool
<name>`, and `--ran <command-substring>` to filter results by indexed activity. A
new `clio activity --by file|command|tool|pattern|url [--since <when>] [--project
<prefix>]` SHALL report counts grouped by the chosen activity kind, most frequent
first.

#### Scenario: Filter sessions by touched file

- **WHEN** the user runs `clio list --touched /x/auth.ts`
- **THEN** only sessions whose tool calls touched a path under `/x/auth.ts` SHALL
  be listed

#### Scenario: Filter search by tool

- **WHEN** the user runs `clio search "race" --tool Bash`
- **THEN** only matches from sessions that ran a `Bash` tool_use SHALL be returned

#### Scenario: Activity grouped by command

- **WHEN** the user runs `clio activity --by command --since 7d`
- **THEN** the system SHALL list commands run in the last 7 days with their counts

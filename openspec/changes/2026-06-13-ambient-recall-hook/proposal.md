## Why

clio's MCP tools are pull-only: Claude recalls past work only when it thinks to
call `search` / `read_session` / `activity_summary`. Starting a fresh session in a
project, that history is invisible until something prompts a lookup. clio has no
way to proactively surface "here's what recently happened in this project" at
session start.

## What Changes

- **Added** `clio recall`: a read-only, fast, project-scoped digest of recent
  activity — the project's most recent sessions (title + date + turns), the files
  it recently touched, and the commands it recently ran — printed as concise text.
  It detects the project from the working directory, opens the index read-only
  (no ingest, no write-lock contention with a running MCP server), prints nothing
  when the project has no indexed history, and exits 0 with empty output on any
  error so it can never break session startup.
- **Added** `clio install-hook` / `clio uninstall-hook`: opt-in, atomic
  registration of a Claude Code SessionStart hook (in `~/.claude/settings.json`)
  that runs `clio recall`, so the digest is injected into every new session in a
  known project. Preserves existing hooks; the write is atomic (the original is
  left intact on failure). Not performed by `install-mcp`.

## Capabilities

### Added Capabilities

- `recall-hook`: a project-scoped recall digest (`clio recall`) and opt-in
  SessionStart-hook installation that injects it at session start.

### Modified Capabilities

- `cli-surface`: adds the `recall`, `install-hook`, and `uninstall-hook` commands.

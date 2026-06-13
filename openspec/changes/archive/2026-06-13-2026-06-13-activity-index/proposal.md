## Why

clio stores each tool call as a single freeform `params_summary` string in
`tool_calls`, with no `tool_name` index and no structured file/command fields.
Users can't answer "which conversations touched `auth.ts`?", "what Bash commands
did I run last week?", or "which sessions used tool X / MCP server Y?". The data
already exists in each message's (redacted) `raw_json` tool_use input — it just
isn't extracted into a queryable shape.

## What Changes

- **Added** an activity index: at ingest, each non-clio tool_use yields structured
  facts into a new `tool_targets(message_id, session_uuid, ts, kind, value)`
  table (the `tool` fact captures the tool name, so no separate column is needed).
  Kinds: `file` (Edit/Write/Read/NotebookEdit/MultiEdit file path),
  `command` (Bash), `pattern` (Grep/Glob), `url` (WebFetch), and `tool` (the tool
  name, including MCP `mcp__server__tool`). clio's own MCP tools stay excluded.
- **Backfill**: a one-time pass populates `tool_targets` from existing
  `messages.raw_json` (no source-file reads), run automatically when the table is
  empty.
- **CLI**: `clio list` and `clio search` gain `--touched <path-prefix>`, `--tool
  <name>`, and `--ran <command-substring>`; a new `clio activity --by
  file|command|tool|pattern|url [--since --project]` reports grouped counts.
- **MCP**: `list_sessions` gains `touched`/`tool` filters; `activity_summary`'s
  `group_by` additionally accepts `file|command|tool|pattern|url`.
- Stored values are already-redacted (file paths survive; command secrets are
  already removed). The existing `tool_calls` table is unchanged.

## Capabilities

### Modified Capabilities

- `session-ingest`: extracts structured activity facts from tool_use into
  `tool_targets`, and backfills existing data once.
- `cli-surface`: `--touched/--tool/--ran` filters and a `clio activity` summary.
- `mcp-server`: activity filters on `list_sessions` and activity grouping in
  `activity_summary`.

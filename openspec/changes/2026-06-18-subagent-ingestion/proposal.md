## Why

Claude Code runs Task-tool subagents and stores each one's transcript as a separate
file under `<project>/<parent-session-uuid>/subagents/agent-<agentId>.jsonl`. clio's
recursive `*.jsonl` walker ingests these files but keys each by its filename
(`agent-<id>`), so every subagent becomes a standalone top-level session,
disconnected from the conversation that spawned it. On a typical machine that is
150+ orphan sessions that clutter the session list, carry no parent link, and
surface in search with no hint they came from a subagent. The
`sessions.parent_session` column (present since the initial schema) has never been
populated. Message-level attribution is already correct; the gap is at the session
level.

## What Changes

- **Added** subagent detection and linking at ingest: a transcript under a
  `subagents/` directory is recognized, its parent session uuid (from the per-line
  `sessionId`, falling back to the parent directory name) is stored in
  `parent_session`, and its type (from `attributionAgent`) in a new
  `sessions.agent_type` column. The subagent keeps its own `agent-<id>` session and
  message attribution; the one-file-one-session incremental machinery is unchanged.
- **Added** a one-time backfill: the migration clears the ingest watermark for
  `subagents/` files so the next index relinks the existing orphan sessions in place
  (no duplicates, no full re-index).
- **Modified** `cli-surface`: `clio list` hides subagent children by default,
  annotates parents with `(+N subagents)`, and adds `--include-subagents`;
  `clio show` lists a parent's subagents (drillable) with `--include-subagents` to
  inline them, and labels a subagent's own transcript with its parent and type;
  `clio search` labels a hit from a subagent with its type.
- **Modified** `mcp-server`: `list_sessions` excludes subagents by default (with an
  `include_subagents` param), `read_session` reports a parent's subagents, and
  `search` results carry `parent_session`/`agent_type` — all read-only.
- **Modified** `tui`: Browse nests subagents under their parent (expand/collapse,
  child count), Search marks subagent hits, and a parent preview can inline its
  subagents.

## Capabilities

### Modified Capabilities

- `session-ingest`: detects subagent transcripts, links them to their parent
  (`parent_session`), records `agent_type`, and backfills existing orphans.
- `cli-surface`: `list`/`show`/`search` become subagent-aware (nesting, `(+N)`,
  `--include-subagents`, subagent header, labeled search hits).
- `mcp-server`: `list_sessions`/`read_session`/`search` become subagent-aware
  (read-only).
- `tui`: Browse nesting + Search subagent labeling + inline preview.

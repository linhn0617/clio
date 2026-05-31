## Why

The README (both `README.md` and `README_zh-TW.md`) advertises `search` as plain
"full-text search" and never explains why clio beats the obvious alternative —
`grep ~/.claude/projects/*.jsonl`. Two things that make clio worth using are
invisible to a reader: most queries are **ranked** by relevance and recency (short queries fall back to a substring scan), and the whole
point is to give a stateless Claude Code session a searchable memory across all
past conversations. A new user cannot weigh clio against grep from the docs alone.

## What Changes

- **Modified** `README.md` and `README_zh-TW.md`:
  - The `search` row in the MCP tools table now states the search is
    recency-aware and **ranked**, not just "full-text search".
  - A new **"Why not just `grep`?"** section (placed before "Getting started")
    contrasts clio with raw grep on four axes: cross-session memory, ranked vs.
    literal hits, signal vs. noise (tool output excluded, snippets trimmed), and
    exact session resolution.
- Documentation only. No code, behavior, CLI flags, or MCP tool semantics change.
  Ranking internals (overscan, role/recency weighting) stay in the code and the
  `fts-search` spec; the README only surfaces that ranking exists.

## Capabilities

### Added Capabilities

- `documentation`: the user-facing README documents that `search` is ranked
  (relevance + recency) and carries a "Why not just grep?" comparison, with the
  English and Traditional Chinese editions kept in sync. No behavioral capability
  (`fts-search`, `mcp-server`, …) changes — this records a documentation
  requirement only.

# clio Subagent Ingestion — Design (Claude Code Task subagents)

- **Date:** 2026-06-18
- **Status:** Approved (brainstorming) — to be implemented via OpenSpec SDD + TDD, then codex + `/review`.
- **Origin:** roadmap feature #5 (multi-agent ingestion), first increment. #5's vision spans **(A)** cross-tool ingestion (Codex/Cursor/Gemini/…) and **(B)** Claude Code subagents. This ships **B first** — smaller, format-native, lower risk. A follows in a later version (v0.9.0).

## Decisions (locked in brainstorming)

1. **Scope = B (Claude Code Task-tool subagents)**, not A (other agents' histories). A is the next version.
2. **Model = child sessions linked to parent** (relational), NOT merge-into-parent and NOT skip. Each subagent transcript stays its own session row; we populate the parent link + type and make consumers parent-aware. Chosen because it reuses the existing `parent_session` column and the one-file-one-session machinery (incremental / seq / purge unchanged), matches Claude Code's own file layout, keeps each subagent a coherent unit, and is the lowest-risk change.
3. **Surface depth = full (β)**: data layer + CLI list/show + MCP + TUI nesting/inline — not data-layer-only.

## Problem (what's wrong today)

Claude Code stores each Task-tool subagent's transcript as a **separate file**:
`<project>/<parent-session-uuid>/subagents/agent-<agentId>.jsonl`. clio's recursive
`*.jsonl` walker ingests these files, and `sessionUUIDFromPath` keys each by its
filename (`agent-<id>`), so every subagent becomes a standalone **top-level** session
disconnected from the conversation that spawned it. On this machine that is **155
orphan sessions** that (a) clutter the session list, (b) carry no parent link, and
(c) surface in search with no indication they came from a subagent. The
`sessions.parent_session` column (present since migration `0001`) has never been
populated. Message-level attribution is already correct (messages do belong to the
`agent-<id>` session); the gap is purely at the **session level**.

## Data model (verified against real logs, read-only)

| Fact | Value |
|---|---|
| Path | `<project>/<parent-uuid>/subagents/agent-<agentId>.jsonl` |
| Parent uuid | the per-line **`sessionId`** field (== the parent dir name; validated 5/5 consistent) |
| Subagent id | **`agentId`** (== filename `agent-<agentId>`) |
| Subagent type | **`attributionAgent`** (e.g. `general-purpose`); `slug` is a per-invocation codename, not the type |
| Title source | the subagent's first user turn = the Task prompt (good natural title) |
| Marker | every line has `isSidechain: true` |
| Corpus | **155** subagent files, **100%** the separate-file format; **0** old inline-`isSidechain` files |

## Architecture

### Detection & extraction (`internal/ingest`)

- A source file is a subagent transcript **iff its parent directory is `subagents/`
  AND its filename is `agent-<id>.jsonl`** (Claude Code's layout). The filename
  prefix guards against a normal session in a project dir that happens to be named
  `subagents`.
- `parentUUID` = inner **`sessionId`** (authoritative); falls back to the parent
  directory name when the field is absent.
- `agentType` = the **first non-empty `attributionAgent`** in the file (else `""`).
- The child session uuid is **unchanged** (= filename `agent-<id>`); message
  attribution is unchanged; the one-file-one-session **seq / watermark / purge**
  machinery is unchanged.

### Schema

- **Migration `0007`**: `ALTER TABLE sessions ADD COLUMN agent_type TEXT;`
- `parent_session` reused (already exists).
- Read-layer `sessions.Session` gains `ParentSession`, `AgentType`; `search.Result`
  gains `ParentSession`, `AgentType` (SELECT/JOIN `sessions`).

### Backfill (existing 155 orphans)

- Migration `0007` also runs `DELETE FROM ingest_state WHERE source_file LIKE
  '%/subagents/%'`, so the next `clio index` re-ingests those files (their bytes are
  unchanged, so incremental would otherwise skip them) and re-UPSERTs the **same**
  `agent-<id>` session rows now carrying `parent_session` + `agent_type`. No row
  deletion, no duplicates.

### Read layer (`internal/sessions`, `internal/search`)

- `ListFilter` gains `IncludeSubagents bool` and `ParentSession string`.
- `ListSessions` lists **top-level only** by default with **orphan promotion**:
  `parent_session IS NULL OR parent_session NOT IN (SELECT uuid FROM sessions)` — a
  child whose parent is absent (never indexed / purged) stays visible, so nothing
  becomes invisible. Given `ParentSession`, it lists that parent's children (for
  nesting). A per-parent subagent **count** is available for the `(+N)` annotation.
- `search` does **not** filter subagents (their content stays findable); results
  carry `parent_session`/`agent_type` so consumers can label them.
- `ActivitySummary` counts `COUNT(DISTINCT COALESCE(parent_session, uuid))` — a
  parent + its subagents count as one session, while subagent messages still count.

### Surfaces (β)

- **CLI `list`**: top-level only by default; parents annotated `(+N subagents)`;
  `--include-subagents` lists children too.
- **CLI `show <parent>`**: trailing `Subagents:` list (id · type · title), drillable;
  `--include-subagents` inlines transcripts. `show <agent-id>`: header `↳ subagent
  (type) of <parent>`.
- **CLI `search`**: a hit from a subagent transcript is labeled with its type
  (`↳<type>`), so it is distinguishable from a top-level conversation.
- **MCP**: `list_sessions` excludes subagents by default (`include_subagents` param);
  `read_session` on a parent reports its subagents (optional inline); `search`
  results carry `parent_session`/`agent_type`. All read-only.
- **TUI**: Browse expand/collapse nesting (indented children; parent shows count);
  Search marks subagent hits with `↳` + type; a parent preview can inline subagents.

## Edge cases & risks

- Inner `sessionId` missing → parent dir name; neither obtainable → no parent (the
  session stays top-level; no crash).
- `attributionAgent` all-empty → `agent_type` `""` → label degrades to `↳ subagent`.
- Parent not yet ingested → orphan promotion keeps the child visible.
- **Watcher**: the recursive walker catches `subagents/` on full / catch-up passes;
  live fsnotify may miss a newly-created `subagents/` subdir — mitigated by the
  periodic recursive catch-up reingest. Verify subdir-watch registration in
  implementation.
- **Purge**: deleting a subagent file purges its child session via the existing
  `source_file` path; unchanged.

## Testing (TDD)

- **Ingest**: a `subagents/` file → `parent_session` = inner `sessionId`,
  `agent_type` = `attributionAgent`; a normal file → no parent; missing `sessionId`
  → falls back to dir name.
- **Read layer**: `ListSessions` default excludes children, orphan promotion,
  `ParentSession` filter; `ActivitySummary` distinct-parent count; search carries
  parent/type.
- **Backfill**: after the migration clears `ingest_state`, re-ingest turns orphans
  into linked children with no duplicate rows.
- **Surfaces**: list `(+N)`; show subagent list / inline; MCP shapes; TUI
  expand/collapse + subagent hit marking (pure `Update` unit tests).
- Whole suite: `go test -race ./...`, `go vet`, windows cross-build,
  `openspec validate --strict`; smoke-test read-only against a **copy** of the real
  index (never the live db).

## Out of scope (this version)

- The old **inline-`isSidechain`** format (0 files on this machine; add only if
  encountered).
- **A: cross-tool ingestion** (Codex / Cursor / Gemini / Copilot / Aider) — next
  version (v0.9.0), building on the agent-identity schema this version establishes.

## 1. Schema & ingest (`internal/db`, `internal/ingest`)

- [x] 1.1 Migration `0007`/`0008` (TDD): add `sessions.agent_type TEXT` (0007);
  clear `ingest_state` for files under `subagents/` (0008) to force a one-time
  relink of existing orphan subagent sessions.
- [x] 1.2 Subagent detection + parent link (TDD): a source under a `subagents/`
  ancestor dir is a subagent transcript; set `model.Session.ParentSession` from the
  per-line `sessionId` (fallback: parent dir name) and `AgentType` from the first
  non-empty `attributionAgent`; ordinary sessions are unaffected.
- [x] 1.3 Persist link (TDD): `upsertSession` writes `parent_session`/`agent_type`
  on both the full and incremental paths; child uuid, seq, watermark, and purge are
  unchanged.

## 2. Read layer (`internal/sessions`, `internal/search`)

- [x] 2.1 Fields + queries (TDD): add `ParentSession`/`AgentType` to
  `sessions.Session` and `search.Result`; SELECT/JOIN them in
  `ListSessions`/`ResolvePrefix`/search.
- [x] 2.2 List nesting (TDD): `ListFilter` gains `IncludeSubagents` and
  `ParentSession`; default lists top-level only with orphan promotion
  (`parent_session IS NULL OR parent_session NOT IN (SELECT uuid FROM sessions)`);
  per-parent subagent counts available.
- [x] 2.3 ActivitySummary (TDD): count `DISTINCT COALESCE(parent_session, uuid)` so a
  parent + its subagents are one session while subagent messages still count.

## 3. Surfaces

- [x] 3.1 CLI `list`/`show`/`search` (TDD): `list` hides children + `(+N subagents)`
  + `--include-subagents`; `show <parent>` lists subagents + `--include-subagents`
  inline; `show <agent-id>` parent/type header; `search` labels subagent hits.
- [x] 3.2 MCP (TDD): `list_sessions` `include_subagents` param + default exclude;
  `read_session` reports a parent's subagents; `search` carries parent/type;
  read-only annotations kept.
- [x] 3.3 TUI (TDD): Browse expand/collapse nesting + child count; selecting a
  nested child previews its transcript; Search subagent `↳` marking — pure `Update`
  unit tests.

## 4. Verify

- [x] 4.1 `go build/vet/test ./...` green (incl. `-race` + windows cross-build);
  `openspec validate --strict`; smoke-tested read-only against a **copy** of the
  real index (179 files indexed → 148 subagents linked, types Explore/Plan/
  general-purpose, 0 orphans; live db untouched).
- [ ] 4.2 Third-party (codex) review of the real implementation diff; fix findings;
  re-review to a clean gate. Then Claude `/review`.

## 1. Schema & ingest (`internal/db`, `internal/ingest`)

- [ ] 1.1 Migration `0007` (TDD): add `sessions.agent_type TEXT`; clear
  `ingest_state` for `source_file LIKE '%/subagents/%'` to force a one-time relink
  of existing orphan subagent sessions.
- [ ] 1.2 Subagent detection + parent link (TDD): a source under a `subagents/`
  ancestor dir is a subagent transcript; set `model.Session.ParentSession` from the
  per-line `sessionId` (fallback: parent dir name) and `AgentType` from the first
  non-empty `attributionAgent`; ordinary sessions are unaffected.
- [ ] 1.3 Persist link (TDD): `upsertSession` writes `parent_session`/`agent_type`
  on both the full and incremental paths; child uuid, seq, watermark, and purge are
  unchanged.

## 2. Read layer (`internal/sessions`, `internal/search`)

- [ ] 2.1 Fields + queries (TDD): add `ParentSession`/`AgentType` to
  `sessions.Session` and `search.Result`; SELECT/JOIN them in
  `ListSessions`/`ResolvePrefix`/search.
- [ ] 2.2 List nesting (TDD): `ListFilter` gains `IncludeSubagents` and
  `ParentSession`; default lists top-level only with orphan promotion
  (`parent_session IS NULL OR parent_session NOT IN (SELECT uuid FROM sessions)`);
  per-parent subagent counts available.
- [ ] 2.3 ActivitySummary (TDD): count `DISTINCT COALESCE(parent_session, uuid)` so a
  parent + its subagents are one session while subagent messages still count.

## 3. Surfaces

- [ ] 3.1 CLI `list`/`show` (TDD): `list` hides children + `(+N subagents)` +
  `--include-subagents`; `show <parent>` lists subagents + `--include-subagents`
  inline; `show <agent-id>` parent/type header.
- [ ] 3.2 MCP (TDD): `list_sessions` `include_subagents` param + default exclude;
  `read_session` reports a parent's subagents; `search` carries parent/type;
  read-only annotations kept.
- [ ] 3.3 TUI (TDD): Browse expand/collapse nesting + child count; Search subagent
  `↳` marking; parent preview inline — pure `Update` unit tests.

## 4. Verify

- [ ] 4.1 `go build/vet/test ./...` green (incl. `-race` + windows cross-build);
  `openspec validate --strict`; smoke-test read-only against a **copy** of the real
  index (never the live db).
- [ ] 4.2 Third-party (codex) review of the real implementation diff; fix findings;
  re-review to a clean gate. Then Claude `/review`.

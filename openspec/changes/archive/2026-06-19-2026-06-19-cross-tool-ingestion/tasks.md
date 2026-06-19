## 1. Source-adapter SPI (`internal/ingest`)

- [ ] 1.1 Define `Source` (`Name`/`Roots`/`Owns`/`SessionIDFromPath`/`NewFileParser`) and
  `FileParser.ParseFrom` (TDD); a registry that validates disjoint roots,
  exactly-one-owner per discovered file, and `Owns(path) ⇒ path under Roots()`.
- [ ] 1.2 Move Claude Code ingestion — `streamParse` + #5-B subagent detection + path
  fallbacks — wholesale into `claudeCodeSource` (TDD: behavior byte-identical; the
  existing ingest suite stays green).
- [ ] 1.3 Generalize `IngestAll`/orchestration to drive any `Source`; keep
  `stat`/`classifyChange`/byte-offset+fingerprint/`commit`/`ingest_state` shared and
  format-agnostic (TDD).

## 2. Schema & identity (`internal/db`, `internal/ingest`)

- [ ] 2.1 Migration `0009`: `ALTER TABLE sessions ADD COLUMN source TEXT`; backfill
  existing rows to `'claude-code'` (TDD).
- [ ] 2.2 Migration `0010`: `source_conflicts` table (TDD).
- [ ] 2.3 Source-aware `commit()`/`upsertSession` (write + validate `source`);
  pre-insert cross-source collision detection → `errSourceConflict`, abort with no rows
  written, upsert/clear `source_conflicts` (TDD).

## 3. Codex adapter (`internal/ingest`)

- [ ] 3.1 `codexSource`: root `~/.codex/sessions` (`config.CodexSessionsDir()`), `Owns`,
  `SessionIDFromPath` = filename uuid; verify it equals `session_meta.payload.id`,
  hard-error + skip on mismatch (TDD, real redacted fixtures).
- [ ] 3.2 Codex `FileParser` (TDD): `response_item` canonical — user (strip
  `<environment_context>`/`<*_instructions>`, drop empty); assistant `output_text`;
  `reasoning`→thinking (non-empty summary); `function_call`→tool_use + lenient
  `ToolTarget`; `function_call_output`→tool_result; SKIP all `event_msg`, `developer`,
  `session_meta`, `turn_context`, `token_count`. `project_path` from `cwd`;
  title/turn_count from user records.
- [ ] 3.3 Register `codexSource` in the source registry.

## 4. Source-aware reads (`internal/sessions`, `internal/search`, `internal/ask`)

- [ ] 4.1 `SourceFilter` (unset ⇒ `claude-code`; `codex`; `all`) (TDD).
- [ ] 4.2 Thread it through `ListSessions`/`ResolvePrefix`/`ActivitySummary`/
  `ActivityByKind`/`search` (query + search)/`ask` retrieval, default Claude-Code-only;
  `sessions.Session` and `search.Result` carry `Source` (TDD).
- [ ] 4.3 `BackfillActivity` skips non-`claude-code` rows (TDD).

## 5. Surfaces

- [ ] 5.1 CLI `list`/`show`/`search`/`ask`/`activity` gain `--source` + source labels;
  `recall` stays CC-only (TDD).
- [ ] 5.2 MCP 5 tools gain a `source` param (default CC) + carry source; read-only
  annotations kept (TDD).
- [ ] 5.3 TUI source label + launch `--source` filter (TDD, pure `Update` unit tests).

## 6. Multi-root operations (`internal/watcher`, `internal/ingest`, `internal/doctor`, `internal/cli`)

- [ ] 6.1 Watcher watches all source roots (TDD).
- [ ] 6.2 `PurgeMissing` partitioned per root; a missing/unavailable root preserves its
  rows and does not authorize purging another source's rows (TDD).
- [ ] 6.3 `doctor` per-source health + `source_conflicts` + unowned-file report;
  `index`/`openForQuery` enumerate source roots (TDD).

## 7. Verify

- [ ] 7.1 `go build/vet/test ./...` green (incl. `-race` + windows cross-build);
  `gofmt -l .` clean; `openspec validate --strict`; smoke-test read-only against a
  **copy** of the real index (absolute `XDG_DATA_HOME` redirect) — live db untouched.
- [ ] 7.2 Third-party (codex) review of the real diff to a clean gate (re-review after
  every fix); then Claude `/review`.

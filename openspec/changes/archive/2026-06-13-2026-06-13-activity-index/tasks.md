## 1. Schema + extraction

- [x] 1.1 Migration `0006`: `tool_targets(message_id, session_uuid, ts, kind,
  value)` table + indexes (`session_uuid`; `(kind, value)`).
- [x] 1.2 Extraction (TDD, unit test per tool type first): from a tool_use
  (name + input) emit facts — a `tool` fact always, plus `file`
  (Edit/Write/Read/NotebookEdit/MultiEdit), `command` (Bash), `pattern`
  (Grep/Glob), or `url` (WebFetch). Exclude clio's own MCP tools. Values redacted
  and capped.
- [x] 1.3 Wire extraction into the ingest commit so new ingests populate
  `tool_targets`; clear rows on full-reingest and purge for the affected session.

## 2. Backfill

- [x] 2.1 One-time backfill from `messages.raw_json` (role=tool_use), idempotent,
  runs when `tool_targets` is empty. No source-file reads. Test on a seeded DB.

## 3. Query surfaces

- [x] 3.1 Read helpers (LIKE-escaped): sessions touching a file prefix; sessions
  filtered by tool; command substring; grouped counts by kind. Tests.
- [x] 3.2 CLI: `--touched/--tool/--ran` on `list` and `search`; `clio activity
  --by file|command|tool|pattern|url`. Tests.
- [x] 3.3 MCP: `list_sessions` `touched`/`tool` params; `activity_summary`
  `group_by` file|command|tool|pattern|url. Tests.

## 4. Verify

- [x] 4.1 `go build ./...`, `go vet ./...`, `go test ./...` green; `openspec
  validate --strict`.
- [x] 4.2 Redaction: no secret leaks into `tool_targets` (test).
- [x] 4.3 Deletion/purge and full-reingest clear `tool_targets` for the session.
- [x] 4.4 Third-party (codex) review of the diff (round 1 caught a P1 + two P2s;
  backfill reworked to be per-message/order-independent under an IMMEDIATE tx;
  round 2 gate PASS).

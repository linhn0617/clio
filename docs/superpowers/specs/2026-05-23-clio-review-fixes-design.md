# clio Review Fixes — Design (4 batches)

- **Date:** 2026-05-23
- **Status:** Approved (brainstorming) — implemented via OpenSpec SDD, one change per batch
- **Origin:** full-codebase review (internal 3-agent + codex third-party), integrated and prioritized.

## Approach

Four independent batches, each delivered as its own OpenSpec change:
propose → gstack/codex plan review → TDD implementation (subagents) →
self-review → codex re-review → merge to main + push (with user approval) → archive.

| Batch | OpenSpec change | Capability | Priority |
|---|---|---|---|
| B1 | `harden-ingest-redaction` | session-ingest | P1 |
| B2 | `improve-fts-search` | fts-search | P1/P2 |
| B3 | `fix-cli-correctness` | cli-surface, diagnostics, mcp-installer | P2 |
| B4 | `polish-review-p3` | (cross-cutting) | P3 |

## B1 — Structured redaction (decided: structured + regex)

Move redaction from regex-on-serialized-text to a structured pipeline over parsed
JSON, keeping regex for free text:
- `redactJSON(raw)`: unmarshal to `any`, recursively walk; a string value whose
  **key name** matches a suspicious set (secret/password/token/apikey/access_key/
  private_key/credential/auth/dsn/conn/url-with-creds) is replaced wholesale; every
  other string value still gets the existing shape-regex pass; re-marshal → `raw_json`.
- Add a connection-string regex (`scheme://user:pass@host`).
- **Session title** is derived from already-redacted text (fixes the title leak).
- Trade-off: re-marshalling `raw_json` may reorder keys/normalize formatting; raw_json
  is for display/replay, not byte-identical fidelity. Acceptable; documented.

## B2 — Search: literal-safe MATCH + hybrid planner (decided)

- `buildMatchQuery`: tokenize, escape `"`→`""`, wrap each term as a `"phrase"`, join
  with space (AND). All literal — `c++`, `"`, `OR`, `(` never error.
- Hybrid planner: terms ≥3 runes drive FTS (candidate rowids); terms <3 runes
  post-filter candidates via `strings.Contains`. All-short queries fall back to LIKE.
- LIKE wildcard escaping (`\ % _` + `ESCAPE '\'`) on content and project_path filters.
- Migration 0004: `idx_sessions_ended` (+ `idx_messages_role_ts`).
- MATCH errors still wrapped → LIKE fallback / friendly message.

## B3 — Correctness / UX

- `ResolvePrefix`: exact-match query first; prefix `LIMIT 2` only if no exact; check `rows.Err()`.
- `show --format raw`: dedup adjacent identical `raw_json`.
- `claudeconfig`: reject (error, leave file untouched) when `mcpServers` exists but is not an object.
- `doctor`: non-zero exit when `!allOK`; capture `Scan` errors → mark check failed.
- `activity_summary`: group by local day (`date(...,'unixepoch','localtime')`).

## B4 — P3 polish (cross-cutting)

`trimToValidUTF8` O(n); large-file read guard/log; `loadExcludedToolUses` + walker
errors logged not swallowed; `show --json` alias; `show` message limit flag (no magic
100000); `projectpath` `_`-collision comment; `XDG_DATA_HOME` absolute-path check;
`.bak` cleanup on chmod failure; `os.IsNotExist`→`errors.Is(fs.ErrNotExist)`;
`format.go` rename; `excluded_tool_uses` per-run cache.

## Testing (every batch)

TDD (failing test first); `go test ./... -race`, `go vet`, windows cross-build; codex
re-review per batch. B1 adds a regression test: title/raw_json/content contain no raw
secret after ingesting a secret-bearing session.

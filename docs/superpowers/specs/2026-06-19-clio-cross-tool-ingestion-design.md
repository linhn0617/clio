# clio #5-A: Cross-Tool Ingestion (Codex CLI) — Design

- **Date:** 2026-06-19
- **Status:** Approved (brainstorming HARD GATE passed; codex-reviewed 4 rounds → implementation-ready, no blocker)
- **Target release:** v0.9.0
- **Builds on:** #5-B subagent ingestion (the `parent_session` / `agent_type` schema, shipped v0.8.0)

## 1. Goal

clio today indexes only Claude Code history (`~/.claude/projects/*.jsonl`). #5-A makes
ingestion **pluggable** and adds OpenAI **Codex CLI** (`~/.codex/sessions`) as a second
source, surfaced behind an **opt-in `--source` filter**, so a single local index can
answer across tools while default behavior stays Claude-Code-only.

First batch is **Codex only**. The architecture is a pluggable *source adapter* SPI so
future tools slot in without re-plumbing. The most likely next source is Google
**Antigravity CLI** (the Gemini CLI successor; Gemini CLI was retired 2026-06-18) — it
writes append-only JSONL at
`~/.gemini/antigravity-cli/brain/<conv-id>/.system_generated/logs/transcript_full.jsonl`,
the same shape Codex uses, so the SPI is designed to fit it. Old Gemini CLI
(`~/.gemini/tmp/*/logs.json`, a whole-file-rewritten JSON array) is a retired tool and is
explicitly **out of scope**.

## 2. Locked decisions

| # | Decision |
|---|----------|
| Scope | Codex CLI only for v0.9.0 (the pluggable SPI + exactly one Codex adapter). |
| Visibility | Codex is **opt-in**. Default = Claude-Code-only on every read surface. `--source claude-code\|codex\|all` (omitted ⇒ `claude-code`) opts in. The DB indexes *all* Codex data regardless; the default simply filters it out. Mirrors #5-B subagent default-hidden semantics. |
| Identity | Add a `source` column to `sessions`; keep `uuid` as the **raw** PRIMARY KEY (not composite, not namespaced). Cross-source uuid collision is handled by **pre-insert detection + loud refusal + a durable record** — never a silent skip. |
| Surface | Full — CLI + all MCP tools + TUI. |

## 3. Codex on-disk format (reverse-engineered from 532 real sessions)

- Path: `~/.codex/sessions/YYYY/MM/DD/rollout-<ISO-ts>-<uuid>.jsonl`. **Append-only JSONL.**
- Every line is an envelope `{ "timestamp", "type", "payload" }`.
  - `type:"session_meta"` — header. `payload.{id, timestamp, cwd, cli_version, git, ...}`.
    `payload.id` is the session uuid and equals the `<uuid>` in the filename. `payload.cwd`
    is the real working directory (no lossy decode needed, unlike Claude Code).
  - `type:"turn_context"` — per-turn config `payload.{turn_id, cwd, model, ...}`.
  - `type:"response_item"` — the API-level conversation stream. `payload.type` ∈:
    - `"message"`: `{role: user|assistant|developer, content:[{type: input_text|output_text, text}]}`.
      `developer` = injected system instructions. `user` messages frequently carry harness
      wrapper blocks (`<environment_context>…<cwd>…`, `<*_instructions>`).
    - `"reasoning"`: `{summary:[…], encrypted_content}`. `summary` is usually empty; `encrypted_content` is opaque.
    - `"function_call"`: `{name, arguments (JSON string), call_id}`. Tool names (`shell`, `apply_patch`, `update_plan`, …) and arg keys differ from Claude Code.
    - `"function_call_output"`: `{call_id, output}`.
  - `type:"event_msg"` — the UI event stream that **duplicates** the conversation:
    `"user_message"{message}`, `"agent_message"{message}`, `"token_count"`, `"task_started"`, …
- Flat `~/.codex/history.jsonl` = `{session_id, ts, text}` cross-session input history — **skipped** (redundant with the rollout files).

### Empirical finding that drives the parse rule (4 real sessions analyzed)

- Interactive session (19 user turns): every real prompt appears in **both**
  `response_item/message(role=user)` (after stripping wrapper blocks) **and**
  `event_msg/user_message`, with identical text. Exactly one wrapper-only `response_item`
  (the env-context injection) had no `event_msg` counterpart.
- `/review` task: `event_msg/user_message` was **duplicated** (same text twice); the
  `response_item` held a differently-shaped `<user_action>` block.
- Other sessions: real prompt in `response_item` (stripped) == `event_msg`, plus one
  wrapper-only `response_item`.

**Conclusion:** `response_item/message(role=user)` is the **guaranteed-complete backbone**
— every real prompt is present there. `event_msg/user_message` is **unreliable**
(duplicates, can diverge, not guaranteed). So `response_item` is canonical and `event_msg`
is skipped entirely; no cross-stream pairing is needed.

## 4. Architecture: pluggable source-adapter SPI

The adapter owns **file identity + whole-file parse/aggregation**, not just line parsing.
The incremental/commit machinery stays shared because it is format-agnostic for any
append-only line-delimited file.

```go
type Source interface {
    Name() string                      // "claude-code" | "codex"
    Roots() ([]string, error)          // dirs to walk + watch (CC: ~/.claude/projects; Codex: ~/.codex/sessions)
    Owns(path string) bool             // routes a discovered/watched file to this adapter
    SessionIDFromPath(path string) string // canonical id for the incremental startSeq lookup
    NewFileParser(startSeq int) FileParser
}

type FileParser interface {
    // Owns the whole streamParse loop: line parse + session-metadata aggregation
    // (id, project_path, title, parent_session, agent_type).
    ParseFrom(r io.Reader, path string, startOffset int64) (
        sess model.Session, msgs []model.Message, consumed, unparsed int64, err error)
}
```

- **Shared & format-agnostic (unchanged):** `os.Stat`, `classifyChange`, byte-offset +
  head/tail fingerprint incremental resume, the message-insert loop, `ingest_state` (keyed
  by absolute path → cross-root unique), FTS triggers, secret redaction.
- **`claudeCodeSource`:** the current `streamParse` + #5-B subagent detection + path
  fallbacks move **wholesale** into its `FileParser` — behavior byte-identical; #5-B must
  not regress.
- **`codexSource`:** its own `FileParser` (section 7).
- **Registry & routing:** `IngestAll`, the watcher, and `PurgeMissing` iterate a registered
  `[]Source`. Each discovered/watched file is routed by `Owns()`. Invariants in §10.

## 5. Schema changes

- **`0009_source_column.sql`:** `ALTER TABLE sessions ADD COLUMN source TEXT;` Existing rows
  backfill to `'claude-code'`; reads treat `NULL`/empty as `claude-code` via `COALESCE`.
  Codex rows are written with `source='codex'`. `uuid` stays PK; all joins
  (`messages.session_uuid`, `tool_targets.session_uuid`, `sessions.parent_session`) unchanged.
- **`0010_source_conflicts.sql`:** durable record for cross-source uuid collisions (§6):
  ```sql
  CREATE TABLE IF NOT EXISTS source_conflicts (
      source_file       TEXT PRIMARY KEY,
      uuid              TEXT NOT NULL,
      seen_source       TEXT NOT NULL,   -- source already owning the uuid
      conflicting_source TEXT NOT NULL,  -- source of the rejected file
      first_seen_at     INTEGER NOT NULL,
      last_seen_at      INTEGER NOT NULL
  );
  ```

## 6. Identity & cross-source collision

`uuid` (raw) stays PK. Both tools emit 122-bit UUIDs, so a real cross-source collision is
effectively impossible — but it must **fail closed**, never silently:

1. Inside `commit()`'s transaction, **before inserting any message or tool_target**, check
   `SELECT source FROM sessions WHERE uuid = ?`. If a row exists with a **different**
   source → return `errSourceConflict`, abort this file's ingest (no rows written), and
   `upsert` into `source_conflicts` (preserve `first_seen_at`, bump `last_seen_at`). The
   file stays on disk un-indexed.
2. On a later successful ingest of that `source_file` (conflict resolved), delete its
   `source_conflicts` row.
3. `doctor` reads `source_conflicts` and reports each unindexed file authoritatively (the
   log line is not the source of truth — the table is).
4. `commit()`/`upsertSession()` become **source-aware**: they write the `source` column and
   source-validate; the full-ingest `ON CONFLICT(uuid) DO UPDATE` and the incremental
   `UPDATE` / `INSERT OR IGNORE` paths all carry and check `source`. ("Shared/unchanged" is
   claimed only for the message-insert loop / `ingest_state` / FTS, not session writes.)

## 7. Codex adapter

- **Root:** `~/.codex/sessions` (recursive `rollout-*.jsonl`). `config.CodexSessionsDir()`.
- **Canonical session id:** `SessionIDFromPath` extracts the uuid from the
  `rollout-<ts>-<uuid>.jsonl` filename — this is the canonical id and the incremental
  `startSeq` key (same model as Claude Code, where the filename uuid is canonical). The
  parser **verifies** it equals `session_meta.payload.id`; on mismatch it **hard-errors and
  skips the file** (logged). No content-override divergence.
- **project_path:** the real `cwd` from `session_meta` / `turn_context` (no lossy decode).
- **Normalization — deterministic, one canonical record type per role, ordered by timestamp, no cross-stream pairing:**

  | Codex record | → clio role |
  |---|---|
  | `response_item/message(role=user)`, wrappers (`<environment_context>`, `<*_instructions>`) stripped; emit if non-empty, else **drop** (pure injection, not a turn) | `user` |
  | `response_item/message(role=assistant)` `output_text` | `assistant` |
  | `response_item/message(role=developer)` | skip (system) |
  | `response_item/reasoning` (only when `summary` non-empty) | `thinking` |
  | `response_item/function_call` (`name` + `arguments`) | `tool_use` + `ToolCall` + lenient `ToolTarget` extraction |
  | `response_item/function_call_output` | `tool_result` |
  | `session_meta` / `turn_context` / `token_count` / **all `event_msg`** | skip |

  This never drops a real prompt (all in `response_item`), never inflates `turn_count`
  (wrapper-only normalizes to empty → dropped), and needs no pairing key.
- **title:** first non-empty user text after stripping. **turn_count:** count of `role=user`
  after the rule above.
- **Tool target extraction:** Codex must extract `tool_targets` **fully at ingest** (Codex
  arg keys differ from Claude Code; extraction is lenient and degrades gracefully). There is
  **no backfill dependency** for Codex rows — see §9 `BackfillActivity`.
- **Incremental:** append-only JSONL → the shared byte-offset/fingerprint machinery is
  reused as-is.
- **Self-pollution:** Codex never calls clio's MCP (clio registers only in Claude Code), so
  the per-source clio-tool exclusion is a no-op for Codex.
- **Redaction:** the existing secret redaction runs for all sources.

## 8. Read / surface layer (opt-in via `--source`, default `claude-code`)

- A `SourceFilter` (unset ⇒ `claude-code`; values `claude-code`, `codex`, `all`) is pushed
  **into the core query APIs**, not patched at the CLI edge:
  `ListSessions`, `ResolvePrefix`, `search` (query.go + search.go), `ActivitySummary`,
  `ActivityByKind`, and the `ask` retrieval path all default to Claude-Code-only.
- `search.Result` and `sessions.Session` carry a `Source` field.
- **CLI:** `search` / `list` / `show` / `ask` / `activity` gain `--source`; each result row
  shows a source label (e.g. `[codex]`); `show` header reports source.
- **MCP (5 tools):** `search`, `ask`, `list_sessions`, `activity_summary`, `read_session`
  gain a `source` param (default `claude-code`) and carry `source` in results.
- **`clio recall`:** **policy CC-only** (it answers "this project's recent activity"). This is
  an explicit, documented exception to the global `SourceFilter` defaulting — there are two
  defaulting mechanisms (global `SourceFilter` default vs `recall` hardcoded), stated so it
  isn't read as one uniform mechanism.
- **TUI:** source label on rows + a launch-time `--source` filter. No in-TUI live source
  toggle (bounded scope, consistent with #5-B's restraint).

## 9. Multi-root operations (partitioned by source root)

- **Watcher:** watches **all** source roots (each `Source.Roots()`).
- **`PurgeMissing`:** becomes **per-root**. It only purges files under a root that is itself
  healthy/available — a healthy Claude root must **not** authorize purging a temporarily
  missing Codex root's rows. Existing safety guards are applied per root.
- **`doctor`:** reports **per-source** state, distinguishing "healthy & reconciled" from
  "rows preserved because root unavailable (purge skipped)"; also reports the count + sample
  of files found under a registered root that **no adapter `Owns()`** (operationally
  significant, surfaced in output, not just debug logs); also reports `source_conflicts`.
- **`config` / `index` / `openForQuery`:** source-aware root enumeration (no longer
  Claude-only hardcoded paths).
- **`install-mcp`:** unchanged (clio still registers only in Claude Code; Codex is just
  another read source).
- `BackfillActivity` (reparses stored `raw_json` as Claude `RawEvent`) becomes
  **source-aware**: it **skips non-`claude-code` rows** (Codex extracts targets at ingest).

## 10. Invariants

- **`parent_session` is claude-code-only.** Codex has no subagents; the Codex parser never
  sets `parent_session`. So `ActivitySummary`'s parent-collapse `LEFT JOIN … parent_session`
  stays correct (parent and child are always same-source).
- **Routing:** exactly one source `Owns()` any discovered path; `Owns(path)` ⟹ `path` is
  under one of that source's declared `Roots()` (enforced at dispatch + asserted in tests);
  roots are disjoint (validated at startup; overlap → startup error).
- **`ingest_state`** keyed by absolute path → cross-root unique.

## 11. codex review trail (provenance)

Four `codex consult` rounds against the real repo. Convergence 7 → 4 → 4 → 0:

- **R1 (7×P1):** SPI seam was wrong (orchestration, not just `ParseLine`, is Claude-specific);
  collision detection too late (messages insert before session upsert); raw-uuid + warn-skip
  = silent loss; #5-B logic lives in `streamParse`, not `ParseLine`; "skip all `event_msg`"
  not defensible; `source` is not presentation-only (every query path is source-blind);
  multi-root purge/watch unsafe.
- **R2 (4×P1 + 4×P2):** Codex filename-id vs `session_meta.id` divergence breaks incremental
  seq; "match per turn" coalesce not implementable without a pairing key; "commit unchanged"
  contradicts source-aware writes; `BackfillActivity` is source-blind; recall + Owns-ambiguity
  + doctor-visibility + ActivitySummary parent-source.
- **R3 (2×P1 + 2×P2):** user-text rule was reactive ("add fallback if a fixture proves it");
  conflict durability needs a concrete table; unowned files shouldn't hide in debug logs;
  `Owns` must imply within-roots.
- **R4:** all resolved; **"v4 is implementation-ready. No final blocker."**

## 12. Testing strategy (TDD)

- Red → green, real **redacted** Codex `rollout` fixtures in `testdata` covering the three
  empirical scenarios (multi-turn incl. CJK, the `/review` `<user_action>` shape, the
  wrapper-only first record).
- Assertions: per-event-type mapping; `event_msg` is skipped; wrapper stripping;
  wrapper-only → dropped (no turn); `reasoning` skipped when summary empty; canonical-id
  mismatch hard-errors; **per-read-path source default = claude-code**; pre-insert
  collision abort + `source_conflicts` row; multi-root purge safety (a missing Codex root
  must NOT purge its rows); 5 MCP tools + `recall` defaults; migration backfill.
- **Never touch the live DB:** all tests run against a **copy** of the index via an absolute
  `XDG_DATA_HOME` redirect.
- `gofmt -l .` (not just `gofmt -w` on touched files) before any push; `go vet`, `go test
  -race ./...`, windows cross-build, `openspec validate --strict` all green.

## 13. Non-goals (YAGNI)

Old Gemini `logs.json` (retired tool); Antigravity CLI (SPI is designed to fit it, but it is
not installed and not built this version); GitHub Copilot CLI (negligible local data);
Codex `history.jsonl`; composite or namespaced primary keys; an in-TUI live source toggle.

## 14. Open implementation risk

The only material risk is ordinary implementation risk: preserving exact Claude Code
behavior while refactoring the ingest / watcher / query plumbing. That is a test/exec
problem (the existing suite + the new source-default tests guard it), not a design hole.

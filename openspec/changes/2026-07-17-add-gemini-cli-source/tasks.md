## 1. Source identity + registry wiring

- [x] 1.1 Add `model.SourceGemini = "gemini"` (`internal/model/model.go:8-9`) and
  `config.GeminiTmpDir()` → `~/.gemini/tmp` (`internal/config/config.go`, mirroring
  `CodexSessionsDir` at :21) (TDD).
- [x] 1.2 Add one `registry.Seed` entry (`internal/registry/registry.go:62`): `Name: "gemini"`,
  `Label: "[gemini]"`, `RootLabel: "gemini chats dir"`, `RootDir: config.GeminiTmpDir`.
  *Acceptance:* with the entry added and **no other surface edited**, `--source gemini` is
  accepted, all five MCP tools' `source` enum includes `gemini`, the TUI labels gemini rows
  `[gemini]`, and `doctor` reports the gemini root — proven by a test that adds the entry and
  checks every derived surface (the registry's existing derivation, `EnumValues`/`Label`/
  `Roots`).

## 2. Whole-file-replay source capability (minimal deferred-cursor slice)

- [x] 2.1 Add a `WholeFileReplay() bool` method to the `Source` interface
  (`internal/ingest/source.go:16`); `claudeCodeSource` and `codexSource` return `false` (TDD).
- [x] 2.2 In `IngestFile` (`internal/ingest/ingest.go:130-234`), when the owning source
  declares whole-file replay and `classifyChange` is not `changeSkip`: force `startOffset=0`,
  `startSeq=0`, `kind=changeFull`, and bypass the fingerprint-resume block
  (`ingest.go:163-197`) (TDD).
  *Acceptance:* claude-code/codex ingest is byte-identical to today (they return `false` →
  same code path); a whole-file source re-ingests the whole file on every change.

## 3. Gemini discovery

- [x] 3.1 `geminiSource` with `Roots()` = `[GeminiTmpDir]` and `Owns(path)` = `.jsonl` under a
  `chats/` ancestor below the root (TDD; the walker `internal/ingest/walker.go:22` is
  untouched — Gemini files are `.jsonl`).
- [x] 3.2 `projects.json` inversion: map the `chats/`-parent directory name (projectId) to an
  absolute project path from `~/.gemini/projects.json`; empty path when the file or the entry
  is missing (TDD).
- [x] 3.3 Old/unsupported layouts (`≤0.1.9` sha256 dirs, no `chats/`, no `projects.json`;
  `logs.json`; `checkpoint-*.json`) own no files and are skipped — asserted, not errored.

## 4. Gemini op-log parser (v1 = observed shapes only)

- [x] 4.1 `SessionIDFromPath` reads the metadata line and returns `sessionId` (full uuid),
  `""` on error (`internal/ingest/source.go`; differs from filename-derived claude/codex ids
  because the main filename carries only an 8-char fragment) (TDD).
- [x] 4.2 `ParseFile` replays from offset 0 **the observed shapes only**: metadata line →
  session; `$set` with `messages` → overwrite (last writer wins); metadata-only `$set` →
  session metadata only. Bare `MessageRecord` and `$rewindTo` records (unobserved shapes) are
  **warn + skip + counted unparsed**, never replayed and never fatal (TDD).
  `debt:` replay branches for bare appends and rewinds (incl. the rewind's
  inclusive-vs-exclusive boundary) are built in task 6.1 once real bytes exist.
- [x] 4.3 **P1 — unusable `$set` aborts the pass**: lines are read via the shared
  `readCappedLine` (16 MiB cap, `internal/ingest/ingest.go:326`); a `$set` line that is
  over-cap or unparsable aborts the whole pass with a warning — nothing is committed, prior
  DB state is preserved verbatim, the failure counts as unparsed, and the file's watermark
  does not advance past the unusable record (so `doctor`'s lag check
  `internal/doctor/doctor.go:309` keeps flagging the file) (TDD).
  `debt:` if a real session ever hits the 16 MiB line cap, revisit (raise cap or stream-parse
  `$set`).
- [x] 4.4 Map reconstructed messages — **text only in v1**: `user`→user (strip
  `<session_context>` wrapper, drop wrapper-only), `gemini`→assistant from `content[].text`;
  a `gemini` message yielding no text via `content[].text` is warn + counted unparsed (never
  indexed as an empty message); `info`/`error`/`warning`→skipped; `thoughts`/`toolCalls` are
  NOT extracted (they stay, redacted, in `raw_json`); `seq` = reconstructed index; content +
  `raw_json` redacted via shared `redactString`/`redactJSON` (TDD).
  `debt:` thinking/tool-use extraction + activity targets built in task 6.1 against real bytes.
- [x] 4.5 `session.source = "gemini"`, `StartedAt`/`EndedAt` = min/max message ts, `Title` via
  shared `titleFrom` on the first non-wrapper user text (TDD).

## 5. Nested files (flat in v1) + purge robustness

- [x] 5.1 A file at `chats/<X>/<child>.jsonl` (parent dir not named `chats`) is indexed as its
  own **flat** session: uuid from its metadata `sessionId`, content searchable, **no
  `parent_session` link and empty `AgentType` in v1** (TDD).
  `debt:` parent linking (parent-dir-name → `ParentSession`, agent type if present) is built
  in task 6.1 once a real nested transcript is observed.
- [x] 5.2 Resolve a path's uuid at purge time from the DB (`SELECT uuid FROM sessions WHERE
  source_file = ?`), falling back to `SessionIDFromPath` — so a deleted Gemini main file (its
  metadata unreadable) still purges cleanly (`sessionUUIDForPurge`, `ingest.go:705-710`) (TDD).

## 6. Fixtures, verification, ship gate

- [ ] 6.1 **SHIP GATE (blocked on user re-auth)** — real-sample re-confirmation: re-auth
  Gemini CLI, drive a real multi-turn session with tool calls + an edit/rewind, then build and
  confirm against real bytes: assistant/`thoughts`/`toolCalls` shapes + extraction,
  bare-`MessageRecord` and `$rewindTo` replay branches (incl. boundary), subagent nesting +
  parent linking, agent-type presence. **This change SHALL NOT be archived, and the gemini
  source SHALL NOT be announced as "supported" in release notes, until this task is done.**
- [x] 6.2 Fixtures: the real v0.51.0 sample (metadata + `$set`) as baseline, plus synthesized
  fixtures for the v1 behaviors — assistant text message, two-`$set` last-writer-wins,
  skip-and-count of a bare `MessageRecord` and a `$rewindTo`, over-cap/unparsable `$set`
  abort, unrecognized-shape assistant message, nested child indexed flat — each header
  commenting observed vs synthesized-from-source-types.
- [x] 6.3 Idempotency test: ingest, append a `$set` editing an earlier message, re-ingest;
  assert the DB rows equal a from-scratch ingest of the final bytes (no duplicate rows, no
  stale rows, correct `turn_count`).
- [x] 6.4 Golden/no-regression: existing claude-code + codex golden tests stay green unchanged
  (proof whole-file-replay did not alter their path).
- [x] 6.5 `go build/vet/test ./...` green (incl. `-race`); `gofmt -l .` clean; `openspec
  validate 2026-07-17-add-gemini-cli-source --strict`; read-only `clio index` + `clio search
  --source gemini` e2e smoke against a **copy** of the real index (absolute `XDG_DATA_HOME`),
  live DB untouched. (A Gemini eval-suite question is deliberately **not** in this change —
  it measures cross-source retrieval quality, not ingest correctness; recorded in design §6
  as a follow-up once real Gemini history exists.)
- [x] 6.6 Third-party (`/codex`) review of the real diff, then Claude `/review`: specifically
  that whole-file-replay leaves claude-code/codex byte-identical, the `$set` replay is
  idempotent, the unusable-`$set` abort preserves prior state, and no new source-name literal
  escaped the registry.

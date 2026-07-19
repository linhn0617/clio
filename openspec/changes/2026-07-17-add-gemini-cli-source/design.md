# Design: add the Gemini CLI source (first real provider on the deferred SPI)

Scope note: this change adds the `gemini` adapter and delivers **only the deferred SPI
generalization Gemini forces** — a per-source whole-file-replay signal (the minimal slice
of the deferred opaque-cursor work, `2026-07-14-generalize-source-adapter-spi/design.md`
§3b). Multi-session-per-file (§3c) and non-`.jsonl` discovery (§3a) stay deferred: Gemini is
one session per file and uses `.jsonl`, so neither seam is needed here. The `IsFallback` /
`SupportsIncremental` / `ExtractsActivity` flag family (§3d) is introduced only in its
single needed member (whole-file replay), per "introduce a flag only when its first consumer
arrives."

## 0. Ground truth vs provisional

Verified on this machine, gemini-cli **v0.51.0** (authoritative; overrides any doc):

- Path: `~/.gemini/tmp/<projectId>/chats/session-<ISO-timestamp>-<id8>.jsonl`, one session
  per file, physically append-only (byte offsets grow monotonically).
- `~/.gemini/projects.json`: `{"projects": {"<abs-project-path>": "<projectId>"}}` — the map
  is path→projectId; the `<projectId>` is the `chats/`-parent directory name.
- Line 0 = session metadata: `{sessionId, projectHash, startTime, lastUpdated, kind:"main"}`.
  `sessionId` is the full uuid; `projectHash` is a sha256 (old-scheme) and is not needed for
  mapping. `kind` is `"main"` for a top-level session.
- Line ≥1 = op-log records. **Observed:** the first user turn arrives as a `$set` record
  `{"$set": {"lastUpdated", "messages": [full messages array]}}`, where each message is
  `{id (hex32), timestamp, type: "user", content: [{text}]}`. The observed first user text
  is entirely a `<session_context>…</session_context>` harness wrapper.

Provisional (from gemini-cli source types `chatRecordingTypes.ts`, **not** in the live
sample — the sample has only metadata + one `$set`):

- `MessageRecord` appended as a bare line (single message, not wrapped in `$set`).
- `type: "gemini"` (assistant) messages carrying `thoughts[]`, `toolCalls[]`, `tokens`,
  `model`; `type: "info" | "error" | "warning"` non-conversational records.
- `$rewindTo: "<messageId>"` rewind records.
- Subagent child files nested at `chats/<parentSessionId>/<sessionId>.jsonl`.

Post-adversarial-review scope ruling (2026-07-17, skeptic + simplifier): **v1 parses only
the observed shapes** (metadata line + `$set` replay; user/gemini textual content). Every
provisional shape above is **not replayed or extracted in v1** — it is skipped defensively
(warn + unparsed count), and its support is built by task 6.1 once real bytes exist.
Task 6.1 is a hard ship gate (§6).

## 1. Discovery — minimal, walker untouched

The deferred discovery generalization (`§3a`: remove the `.jsonl` filter from
`WalkSessionFiles`, `internal/ingest/walker.go:22`) is **not needed**: Gemini files are
`.jsonl`, so the existing walker already enumerates them. Evaluated and rejected touching
the walker — it would be change-for-change's-sake with no Gemini payoff and four callers to
re-verify (`ingest.go:84,94`, `watcher.go:107`, `doctor.go:342`).

- **Root:** `config.GeminiTmpDir()` = `~/.gemini/tmp`, returned from the adapter's `Roots()`
  so `IngestAll`/watcher/purge span it. The recursive walker finds both main files
  (`chats/session-*.jsonl`) and nested subagent files (`chats/<parent>/<child>.jsonl`).
  `logs.json` and `checkpoint-*.json` are `.json`, so the walker skips them for free.
- **Ownership (`Owns`):** a path ends in `.jsonl` **and** has an ancestor directory named
  `chats` under the Gemini root. This covers main + subagent files and is well-defined, so
  the claude-code fallback (which owns *any* `.jsonl`, `source.go:47`) never claims a Gemini
  transcript (the Gemini adapter is prepended, `ingest.go:66-69`, so its `Owns` is consulted
  first). A stray non-`chats` `.jsonl` under `~/.gemini/tmp` (none exist in practice) would
  fall to the claude-code fallback and parse to an empty session — the same benign behavior
  the fallback already has everywhere; the `chats/`-ancestor rule keeps the owned set tight.
- **projectId → project_path:** invert `~/.gemini/projects.json` (values are projectIds,
  keys are absolute paths) keyed by the `chats/`-parent directory name. Missing
  `projects.json`, or a projectId with no entry (e.g. a `≤0.1.9` sha256-hash dir), yields an
  empty project path — the session is still indexed, just unattributed. We do **not** parse
  the project path out of the `<session_context>` prose (fragile); `projects.json` is the one
  structured source.
- **Old / unsupported layouts:** `≤0.1.9` installs have no `chats/` dir and no
  `projects.json`; they own no files (no `chats` ancestor) and are silently skipped. This is
  a non-goal, not an error.

## 2. Session identity — the id is not in the (main) filename

Claude Code and Codex derive the canonical uuid from the filename (`walker.go:36`,
`codex.go:40`). A Gemini **main** filename carries only an 8-char fragment
(`session-<ts>-<id8>.jsonl`), so `SessionIDFromPath` cannot return the full uuid from the
path alone.

- **Canonical uuid = metadata `sessionId`.** `SessionIDFromPath(path)` reads the file's
  first line and returns its `sessionId` (falling back to `""` on read/parse error). Reading
  ~200 bytes per file is cheap; it is called once per file per ingest pass. (Subagent child
  files happen to be named `<full-sessionId>.jsonl`, but reading the metadata line works
  uniformly and is the single code path.)
- **Purge of a deleted file:** once a main file is deleted, its metadata line is unreadable,
  so `SessionIDFromPath` cannot recover the full uuid — but purge needs it to delete
  `sessions`/`messages` rows (`ingest.go:712-755`). Resolve the uuid at purge time from the
  DB: `SELECT uuid FROM sessions WHERE source_file = ?`, falling back to
  `SessionIDFromPath`. This is a small, source-agnostic tweak to `sessionUUIDForPurge`
  (`ingest.go:705-710`) and is the concrete reason the deferred "id is in the filename"
  assumption needed loosening. It is safe for claude-code/codex too (DB and filename agree
  there).

## 3. Parser — $set op-log replay (v1 = observed shapes only)

Unlike the claude/codex parsers (`parser.go:60`, `codex.go:91`) where one line → zero-or-more
messages appended in stream order, a Gemini file must be **replayed** to a final state. v1
replays only the shapes observed in the real sample:

```
state.messages = []            // reconstructed conversation
for each record after line 0, in file order:
  if record is {"$set": {messages: M, ...}}:   state.messages = M   // full overwrite, last writer wins
  // metadata-only $set (no messages key): updates lastUpdated etc., leaves messages
  // anything else (bare MessageRecord, $rewindTo, unknown): warn + skip + unparsed++
```

Replay rules:

- **Order matters, last writer wins.** A later `$set` replaces everything an earlier `$set`
  built. This is why partial "resume from byte offset" is incoherent (§4).
- **Unobserved shapes are not replayed in v1** (adversarial-review ruling, §0): a bare
  `MessageRecord` append and a `$rewindTo` are warn+skip+unparsed — visible to `doctor`,
  never fatal, never guessed at.
  `debt:` v1 does not replay bare-`MessageRecord` appends or `$rewindTo` rewinds (a session
  containing them indexes only its `$set`-carried state, with the skips counted); build both
  branches — including the rewind's inclusive-vs-exclusive boundary decision — in task 6.1
  when a real transcript containing them exists.
- **Unusable `$set` = abort, not skip (P1).** Lines are read via the shared `readCappedLine`
  (16 MiB cap, `incremental.go:15`, `ingest.go:326`). An ordinary skipped line loses one
  record; a `$set` line that is over-cap or unparsable carries the *entire* conversation
  state, so skip-and-continue would silently discard the whole update. Instead the pass
  **aborts with a warning and commits nothing**: prior DB state is preserved verbatim, the
  failure is counted as unparsed, and the file's watermark is not advanced past the unusable
  record — so `doctor`'s lag check (`doctor.go:309`, `fi.Size() > offset`) keeps flagging the
  file instead of it going silently green-stale.
  `debt:` if a real session ever exceeds the 16 MiB line cap, revisit (raise the cap or
  stream-parse `$set` records); until observed, abort-and-flag is the loss-free choice.

Mapping the reconstructed `messages[]` to clio messages (deterministic order; `seq` =
running index from 0). v1 maps **textual conversation only**:

- `type: "user"` → `RoleUser`. Join `content[].text`. **Strip `<session_context>` wrapper
  blocks** and drop the message if wrapper-only (mirrors codex's `<environment_context>`
  handling, `codex.go:248-280`), so the harness preamble is neither a turn nor the title.
- `type: "gemini"` → `RoleAssistant`, from `content[].text`. **Defense:** if the content
  yields no text via `content[].text` (unrecognized shape), warn + count unparsed and index
  nothing — never silently index an empty assistant message.
- `thoughts[]` / `toolCalls[]` on a `gemini` message → **not extracted in v1**. Their field
  shapes are unobserved; codex's v1 did rich extraction because it had real samples — Gemini
  does not. The fields remain (redacted) in the stored `raw_json`.
  `debt:` no thinking/tool-use messages and no activity targets from Gemini in v1; build the
  extraction (mirroring `codexExtractTargets`, `codex.go:378`, incl. the `mcp__clio__*`
  self-pollution guard) in task 6.1 against real bytes.
- `type: "info" | "error" | "warning"` → skipped (non-conversational).
- **Redaction reuse:** content and `raw_json` go through the shared `redactString` /
  `redactJSON` (`redact.go:105,154`); the per-message `raw_json` is the record's line, so
  `clio show --format raw` stays redacted. Title via the shared `titleFrom`
  (`parser.go:171`). No Gemini-specific redaction.
- **Timestamps:** each message's `timestamp` (RFC3339) via the shared `parseTS`
  (`parser.go:138`); session `StartedAt`/`EndedAt` are the min/max, as elsewhere.

## 4. The hard point — incremental × replay, and how idempotency is kept

**Problem.** The shared orchestrator (`ingest.go:130-234`) resumes append-only files from a
stored byte offset: `classifyChange` (`incremental.go:56`) sees the file grew → `changeIncremental`,
it fingerprints the prefix, seeks to `LastByteOffset`, parses only the new tail at
`startSeq = maxSeq+1`, and `commit` **INSERT-OR-IGNOREs** without deleting prior rows
(`ingest.go:437-483`). That is correct only when new bytes = new messages appended at the
end. For a Gemini op-log it is **unsound**:

- A `$set` appended after the offset carries the *entire* new `messages[]`. Parsing only the
  tail and emitting that array at `maxSeq+1` would **duplicate** every prior message at fresh
  seqs (doubling `turn_count`, corrupting content).
- A `$rewindTo` appended after the offset should **remove** already-ingested messages. An
  incremental pass can only INSERT (or INSERT-OR-IGNORE); it can never delete, so the stale
  rows would survive.
- Even detecting `$set`/`$rewindTo` in the new tail does not rescue a partial resume: a
  `$set` needs no prior state (it is self-contained) but a bare `MessageRecord` append needs
  the full prior `messages[]` to know the running index — so a mixed op-log cannot be
  reconstructed from an arbitrary byte offset without re-reading from 0.

**Decision (recommended): unconditional whole-file replay for Gemini.** Whenever a Gemini
file has changed, re-read it from offset 0, replay the whole op-log, emit the reconstructed
messages at deterministic `seq = index`, and commit as a **full re-ingest** (`changeFull`:
delete this uuid's messages/tool rows, then insert, `ingest.go:437-447`). The stored byte
offset/fingerprints are kept **only as a change-detector** (so an unchanged file is skipped
by `classifyChange` → `changeSkip`, no reparse), never as a resume point.

Why not the narrower "reparse whole file only when a `$set`/`$rewindTo` appears in the new
tail":

- To know whether the tail contains a `$set`/`$rewindTo` you must read the tail — at which
  point, for a `$set`, you already hold the full state, and for a bare append you still need
  the prior array. The branch buys a micro-optimization (skip re-reading earlier lines) at
  the cost of two parse modes and a subtle idempotency argument. Rejected as false economy.
- Unconditional replay makes idempotency trivial and inspectable: same bytes → same
  reconstructed state → same rows (full-replace commit), and a shrink/edit is reflected
  because the commit deletes first. This is the property the spec pins.

**Honest cost of this decision (adversarial-review P2).** For an actively-written session,
every watcher batch (the watcher fires ingest 500ms after the first dirty event of a batch,
`watcher.go:20-24`) triggers a full delete-and-reinsert of that session's rows plus the FTS
delete/insert trigger work — cost grows **linearly with session length** on every change,
where the append-only sources pay only for the new tail. This is accepted deliberately:
correctness under `$set`/rewind semantics cannot be had cheaper without the two-mode
complexity rejected above, and Gemini sessions are bounded by a single conversation.
`debt:` if real-world use shows re-ingest thrash on long active Gemini sessions, revisit
with a size threshold or watcher-side coalescing for whole-file-replay sources; do not
pre-build either without observed thrash.

**Read-side stability under changeFull row churn (skeptic-verified, recorded to close the
question).** A full re-ingest deletes and reinserts message rows, churning their SQLite
rowids — but `read_session` pagination is `offset`/`limit` over `ORDER BY seq`
(`internal/mcp/tools.go:205-207`, `internal/sessions/sessions.go:223`), and `seq` is
deterministically reassigned from 0 on every replay. Row-id churn therefore never affects
read pagination; a page read concurrent with a re-ingest sees a consistent seq-ordered
snapshot (SQLite transaction) of either the old or new state.

**How this is wired with minimal blast radius.** Add one signal to the `Source` interface
(`source.go:16`) — e.g. `WholeFileReplay() bool` (claude-code/codex return false, gemini
returns true). In `IngestFile`, when the owning source declares whole-file replay and the
file is not `changeSkip`: force `startOffset = 0`, `startSeq = 0`, `kind = changeFull`, and
bypass the fingerprint-resume block (`ingest.go:163-197`). Everything downstream
(`commit`, `ingest_state`, cross-source collision, FTS, purge) is unchanged. Store
`LastByteOffset = newOffset = res.Consumed` (≈ file size) so `doctor`'s `reconcile` lag check
(`doctor.go:309`, `fi.Size() > offset`) does not false-alarm; `truncated`/`rewritten`
(`doctor.go:302-308`) never fire because the physical file only grows. **`doctor` needs no
change.** Golden tests assert claude-code/codex take the exact same path as today (they
return false), so their behavior is byte-identical.

## 5. Nested files — flat indexing in v1

Gemini reportedly nests a subagent transcript at `chats/<parentSessionId>/<childSessionId>.jsonl`
(provisional, unobserved). Adversarial-review ruling: **v1 indexes nested files flat** —
the `Owns` rule (§1) already claims them (any `.jsonl` under a `chats/` ancestor), each is
indexed as its own session with its own metadata `sessionId` as uuid, its content fully
searchable. No `parent_session` link is recorded in v1:

- Linking would encode an unobserved layout into `parent_session` semantics; a wrong guess
  pollutes the parent/child views the TUI and `list_sessions` build on
  (`model.Session.ParentSession` / `AgentType`, `model.go:31-32`).
  `debt:` nested Gemini children are indexed but not linked to their parent (and `AgentType`
  stays empty); build the linking — parent-dir-name → `ParentSession`, agent-type if the
  child metadata carries one — in task 6.1 once a real nested transcript is observed.
- `kind: "main"` in metadata corroborates a top-level session; a main file never gets a
  parent link in any version.
- Per-`(source, uuid)` conflict handling is unchanged — a nested child is a normal session
  row keyed by its own uuid.

## 6. Testing strategy + ship gate

- **TDD**, mirroring `codex_test.go`. Fixtures: the real v0.51.0 sample (metadata + `$set`,
  no assistant) as the baseline, plus **synthesized** fixtures exercising the v1 behaviors:
  a `gemini` assistant text message, two-`$set` last-writer-wins, skip-and-count of a bare
  `MessageRecord` and a `$rewindTo`, an over-cap/unparsable `$set` abort, an
  unrecognized-shape assistant message, and a nested child indexed flat — each fixture
  header commenting **what is observed vs synthesized from source types**.
- **Golden / no-regression:** the existing claude-code + codex golden tests
  (`2026-07-14` change, tasks §3) stay green unchanged — proof the whole-file-replay signal
  did not alter their path. A fake-plus-real check asserts the single `gemini` Seed entry
  surfaces in `--source`, MCP enum, TUI label, and `doctor` with no per-surface edits.
- **Idempotency test (the crux):** ingest a fixture, append a `$set` that edits an earlier
  message, re-ingest, assert the DB rows equal a from-scratch ingest of the final bytes (no
  duplicates, no stale rows, `turn_count` correct).
- **e2e smoke:** read-only `clio index` + `clio search --source gemini` against a **copy** of
  the real index (absolute `XDG_DATA_HOME` redirect), live DB untouched.
- **6.1 — real-sample re-confirmation, a hard ship gate:** re-auth Gemini CLI, drive a real
  multi-turn session with tool calls and an edit/rewind, then build/confirm against real
  bytes: assistant/thoughts/toolCalls field shapes (extraction), bare-`MessageRecord` and
  `$rewindTo` replay branches (incl. the inclusive-vs-exclusive boundary), subagent nesting
  + parent linking, agent-type presence. **Until 6.1 is done, this change is not archived
  and the gemini source is not announced as "supported" in release notes.** Blocked on user
  re-auth (marked in tasks.md).
- **eval suite (moved out of this change):** adding a Gemini retrieval question measures
  cross-source retrieval quality, not ingest correctness — recorded as a follow-up change
  once real Gemini history exists to query.

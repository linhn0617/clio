## Why

clio ingests Claude Code and OpenAI Codex CLI history. Both existing adapters are the
same shape — append-only JSONL, one session per file, the session id in the filename,
one transcript line = one indexable event. The source registry
(`2026-07-14-generalize-source-adapter-spi`, `internal/registry/registry.go`) removed the
`{claude-code, codex, all}` identity duplication so every read surface derives from a
compile-time seed, but it deliberately **deferred** the structural SPI generalizations
(discovery, incremental cursor, one-file-one-session) because two same-shape adapters are
not enough evidence to fix those seams (`design.md` §3 of that change).

Gemini CLI is the first real provider that exercises those seams. Its current
`ChatRecordingService` writes full transcripts (user + assistant + thoughts + tool calls)
as JSONL under `~/.gemini/tmp/<projectId>/chats/session-<ISO>-<id8>.jsonl`, one session per
file, physically append-only — but the file is an **op-log**, not a 1:1 event stream: a
record can be a full-state overwrite (`$set`), a single appended message, or a rewind
(`$rewindTo`). Replaying it to reconstruct the final message list breaks the SPI's
byte-offset "resume from where we left off" assumption, because new bytes after the cursor
can rewrite or truncate already-ingested state. Gemini also breaks the "session id is in
the filename" assumption: a main-session filename carries only an 8-char id fragment; the
canonical id lives in the file's first (metadata) line.

This change adds Gemini as a third source and delivers **only the deferred generalizations
Gemini actually needs** — a per-source "whole-file replay" signal (the minimal slice of the
deferred opaque-cursor work) — leaving multi-session-per-file and non-`.jsonl` discovery
still deferred (Gemini files are `.jsonl`, so the walker is untouched).

Ground truth: format verified against gemini-cli v0.51.0 source and a live v0.51.0 sample on
this machine (`~/.gemini/tmp/gemini-sample/chats/session-2026-07-17T14-18-4ac5c3df.jsonl`,
`~/.gemini/projects.json`). Scope ruling after adversarial review (2026-07-17): **v1 parses
only the shapes observed in the live sample** (metadata line + `$set` replay; user/gemini
text). Shapes known only from gemini-cli source types (bare `MessageRecord`, `$rewindTo`,
`thoughts`/`toolCalls`, nested subagent files) are skipped defensively in v1 and built by
the real-sample re-confirmation task (tasks.md 6.1), which is a **hard ship gate**: this
change is not archived, and gemini is not announced as supported, until that task passes
against a real assistant-bearing sample.

## What Changes

- **Added** the `gemini` source: a `model.SourceGemini = "gemini"` constant, a
  `config.GeminiTmpDir()` resolver (`~/.gemini/tmp`), and one `registry.Seed` entry
  (`name: gemini`, `label: [gemini]`, `RootLabel: gemini chats dir`, `RootDir:
  GeminiTmpDir`). Because every read surface already derives from the registry, this single
  entry wires `--source gemini`, the MCP `source` enum/param, the TUI `[gemini]` label, and
  `doctor`'s per-source report with no per-surface edits.
- **Added** Gemini discovery: the adapter owns `*.jsonl` files under any `chats/`
  directory below `~/.gemini/tmp`, and maps a transcript's parent-project id (the
  `chats/`-parent directory name) to a real project path by inverting
  `~/.gemini/projects.json`. Old layouts (`≤0.1.9` sha256-hash dirs with no `chats/`, and
  `logs.json`) own no files and are silently skipped.
- **Added** the Gemini op-log parser (v1 = observed shapes only): it replays the metadata
  line + `$set` overwrites (last writer wins) to reconstruct the final `messages[]`, then
  maps `user`→user and `gemini`→assistant **text**, stripping `<session_context>` harness
  wrapper blocks. Unobserved record shapes (bare `MessageRecord`, `$rewindTo`) are
  warn+skip+counted-unparsed, and `thoughts`/`toolCalls` are not extracted in v1 (they stay,
  redacted, in `raw_json`) — both built by the gated task 6.1 against real bytes. An
  over-cap or unparsable `$set` (which carries the whole conversation state) aborts the pass
  preserving prior indexed state, visible to `doctor`, never a silent partial replay. Secret
  redaction is the shared machinery, unchanged.
- **Added** a per-source **whole-file-replay** capability (the minimal deferred-cursor
  slice): a source may declare that it is reconstructed by full replay rather than
  byte-offset resume. For such a source the orchestrator parses from offset 0 and commits
  as a full re-ingest on every change, so a `$set` overwrite (and, once task 6.1 builds it,
  a `$rewindTo` shrink) stays idempotent; the stored byte offset is used only as a
  change-detector, never a resume point. Claude Code and Codex keep byte-offset incremental resume unchanged.
- **Modified** the "Ingestion routes through pluggable source adapters" requirement to
  allow a source to opt out of byte-offset incremental resume while keeping every other
  shared mechanism (commit transaction, `ingest_state`, FTS, redaction, cross-source
  collision handling) format-agnostic.

## Capabilities

### Modified Capabilities

- `session-ingest`: adds Gemini discovery, the `$set` op-log replay parser (with the
  unusable-`$set` abort rule), the whole-file-replay source capability, Gemini session
  identity / project-path mapping, and flat indexing of nested Gemini transcripts (parent
  linking gated on task 6.1); modifies the pluggable-adapter requirement to permit
  whole-file-replay sources.

## Non-goals

- **Old Gemini `logs.json`** (user-only, whole-file-rewritten, many sessions per file) is
  not indexed — it yields only half the conversation and is superseded by `chats/*.jsonl`.
- **`≤0.1.9` layouts** (sha256-hash project dirs, no `chats/`, no `projects.json`) are not
  supported; such installs own no files and are skipped.
- **Gemini `/chat save` checkpoint files** (`checkpoint-<tag>.json`) are redundant with the
  chats stream and not indexed.
- **Aider** (Markdown, many sessions per file) and **Cursor** (SQLite) remain deferred —
  Gemini does not exercise non-`.jsonl` discovery or one-file-many-sessions, so those seams
  stay unbuilt (`design.md` §3 of `2026-07-14-generalize-source-adapter-spi`).
- No change to the `Source` interface beyond the whole-file-replay signal; no change to the
  schema, FTS, redaction, or the read-side source filtering already derived from the
  registry.

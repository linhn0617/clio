# Changelog

All notable changes to clio are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.7.0] - 2026-06-16

Browse and search your history without leaving the terminal. `clio tui` opens a
full-screen dashboard over the same index the CLI and MCP server read — live
search, recent sessions, an activity overview, and question-answering — each with
a master-detail preview of the matched conversation. Read-only: nothing is written
to the index while you browse.

### Added

- `clio tui`: an interactive Bubble Tea dashboard with four tabs — **Search**
  (debounced live search; the selected hit is windowed and highlighted in its
  surrounding turns), **Browse** (recent sessions, optional `--project`), **Activity**
  (top files / commands / tools, drilling into the sessions behind each entry), and
  **Ask** (`ask` evidence groups with their windowed excerpts). `Tab`/`Shift-Tab` or
  `1`-`4` switch tabs; `↑↓` / `j k` navigate (lists scroll to keep the selection in
  view); `Esc` or `Ctrl-C` quits. On Search and Ask the focused input takes `q` and
  the digits as query text. Opens like `search` (incremental catch-up; defers to a
  running MCP server) and, when no index exists, exits with a hint to run `clio index`.

### Changed

- `sessions.GetMessages` / `GetWindow` take an `includeRaw` flag so the TUI preview
  skips the `raw_json` column it never renders, keeping selection-driven loads light
  on a large index. (internal)

## [0.6.0] - 2026-06-14

Ask a question, get a cited answer from your own history. `clio ask` retrieves the
conversation excerpts most relevant to a question — each windowed in its
surrounding turns and grouped by session — for you (or Claude over MCP) to
synthesize from. clio generates nothing and makes no network call; retrieval stays
local.

### Added

- `clio ask "<question>"`: a retrieval-only, cited evidence bundle over indexed
  history. It extracts content terms (bilingual; an unspaced CJK question expands
  to trigrams for the FTS index plus bigrams for the substring fallback, split on
  stopwords), retrieves any-term matches with full-term (FTS) hits ranked ahead of
  substring-only (LIKE) hits, groups them by session, and windows each hit in its
  surrounding user/assistant turns. Flags: `--project` (default all projects),
  `--since`, `--limit`, `--window`, `--json`. Reads the index like `search`
  (incremental catch-up; defers to a running MCP server).
- MCP: an `ask` tool returning the same bundle as structured JSON, so Claude
  synthesizes the answer from grounded excerpts and cites session ids. Read-only.

## [0.5.0] - 2026-06-13

Ambient recall: an opt-in SessionStart hook that opens each new Claude Code
session with a digest of what recently happened in the project — so past work is
surfaced proactively, without Claude having to think to search for it first.

### Added

- `clio recall`: a read-only, project-scoped digest of recent activity — the
  project's most recent sessions (title, date, turns), the files it recently
  touched, and the commands it recently ran. It detects the project from the
  working directory (walking up to the repo root), opens the index read-only (no
  ingest, no write-lock contention with a running MCP server), prints nothing
  when the project has no indexed history, and exits 0 with empty output on any
  error so it can never break session startup.
- `clio install-hook` / `clio uninstall-hook`: opt-in, atomic registration of a
  Claude Code SessionStart hook (in `~/.claude/settings.json`) that runs
  `clio recall`. Preserves your existing hooks (removes only clio's own entry on
  uninstall) and is separate from `install-mcp`.

## [0.4.0] - 2026-06-13

An activity index over your tool calls: search and summarize which files past
sessions touched, which commands they ran, and which tools they used. The database
migration and a one-time backfill apply automatically on next run; no forced reindex.

### Added

- Activity index. At ingest, each tool call records structured facts — files
  touched (Edit/Write/Read/NotebookEdit/MultiEdit), commands run (Bash), search
  patterns (Grep/Glob), fetched URLs (WebFetch), and the tool used (including MCP
  servers). Existing history is backfilled automatically from stored events,
  without re-reading source files.
- CLI: `clio activity --by file|command|tool|pattern|url` summaries, plus
  `--touched <path>`, `--tool <name>`, and `--ran <substring>` filters on
  `clio list` and `clio search`.
- MCP: `list_sessions` gains `touched`/`tool`/`ran` filters, and `activity_summary`
  `group_by` gains `file|command|tool|pattern|url`, so Claude can answer "which
  conversations touched `auth.ts`?" or "what commands did I run last week?".
- README: a "Why not just `grep`?" section, and the `search` tool now documents its
  recency-aware ranking.

## [0.3.0] - 2026-05-25

Privacy hardening and more reliable indexing. Backward-compatible; the database
migration applies automatically on next run with no forced reindex.

### Security

- Redact `Authorization: Basic` / `Proxy-Authorization` credentials and
  `Cookie:` / `Set-Cookie:` header values at ingest time, plus the
  `authorization` / `cookie` JSON keys. Pasted curl commands and HTTP dumps no
  longer leak these credentials into the searchable index or `clio show`.
- Enforce `0600` on the database `-wal`/`-shm` sidecars (not just the main DB file).

### Added

- Deleted source files are purged from the index. Removing a session file under
  `~/.claude/projects/` now removes it from search, reconciled by the MCP watcher
  backstop, CLI catch-up, and at MCP leader startup. Guards prevent a transient
  filesystem outage (or a renamed/moved file) from wiping live data.
- `clio doctor` now checks private-file permissions (DB + `-wal`/`-shm` are `0600`)
  and reports the count of source lines that could not be parsed.
- `ingest_state` tracks per-source unparseable-line counts (migration `0005`,
  applied automatically).

### Changed

- Incremental ingest streams complete lines with bounded memory and a per-line
  size cap instead of loading the whole unread tail, so a giant or corrupt line
  can no longer exhaust memory.
- Rewrite detection validates both a head and tail fingerprint; a same-size
  rewrite is now treated as a rewrite (full reingest) rather than a no-op.
- `context.Context` is threaded through the read data layer (search / sessions).
- `clio install-mcp` / `uninstall-mcp` edits to `~/.claude.json` are serialized
  across processes (Unix), so concurrent runs cannot lost-update each other.
- DB tuning: `cache_size` pragma and a post-migration `PRAGMA optimize`; search
  re-ranking widened for more accurate recency-aware ordering.

### Fixed

- Complete-but-unparseable lines are counted and surfaced via `clio doctor`
  instead of being silently dropped from the index.
- Ingest aborts a commit when the source file can no longer be validated
  (removed or replaced mid-ingest) instead of writing a stale snapshot.
- `pidAlive` treats `EPERM` (a live process owned by another user) as alive, so a
  valid MCP lease is not wrongly taken over.

## [0.2.0] - 2026-05-24

Safer search, stronger redaction, and a batch of correctness fixes. No breaking
changes.

### Added

- `clio show --json` (alias for `--format json`) and `--limit N`.
- Index on `sessions.ended_at`.

### Changed

- Operator-safe full-text search: FTS-special input (`c++`, `"unclosed`,
  `foo OR`, `(test`) matches as literal text instead of raising an FTS5 error.
- Hybrid query planner: 3+ character terms drive the FTS index while shorter
  terms (including 2-character CJK) filter the narrowed rows, avoiding a
  full-table scan when any single term is short.
- Structured, JSON-aware secret redaction replaces regex-on-serialized-text: a
  secret under a suspicious key is redacted as a whole subtree, and JSON embedded
  anywhere in a message is handled (fail-closed on pathological nesting depth).
- `activity_summary` day buckets use the local calendar day, not UTC.

### Fixed

- Session titles can no longer contain a raw secret (the first-user-message title
  is derived from already-redacted text); both `content` and `raw_json` covered.
- Correct `LIKE` escaping for `%`/`_` in content terms and `--project` prefixes.
- `clio doctor` exits non-zero when a check fails and no longer hides query errors
  behind a passing/zero-count result.
- Session-id resolution returns the exact match even when it is also a prefix of
  other ids.
- `clio show --format raw` no longer prints an event line once per expanded message.
- `install-mcp` refuses to overwrite a non-object `mcpServers` (and survives a
  `null` config) instead of clobbering it, and never leaves a stray `.bak`.
- `XDG_DATA_HOME` is honored only when absolute; faster UTF-8-safe truncation.

## [0.1.0] - 2026-05-23

Initial release.

### Added

- Index Claude Code session files (`~/.claude/projects/**/*.jsonl`) into a local
  SQLite + FTS5 database; local-first and read-only against the source files.
- CLI: `clio search`, `list`, `show`, `index`, `doctor`, `install-mcp`,
  `uninstall-mcp`.
- MCP server exposing `search`, `list_sessions`, `activity_summary`, and
  `read_session` so Claude Code can query its own history in-session.
- A file watcher that live-ingests new activity while the MCP server runs, with a
  periodic full-walk backstop; incremental catch-up on CLI commands otherwise.
- Secret redaction at ingest time (API keys, tokens, private keys, `.env`-style
  lines), in both the searchable text and the stored raw event.

[0.6.0]: https://github.com/linhn0617/clio/releases/tag/v0.6.0
[0.5.0]: https://github.com/linhn0617/clio/releases/tag/v0.5.0
[0.4.0]: https://github.com/linhn0617/clio/releases/tag/v0.4.0
[0.3.0]: https://github.com/linhn0617/clio/releases/tag/v0.3.0
[0.2.0]: https://github.com/linhn0617/clio/releases/tag/v0.2.0
[0.1.0]: https://github.com/linhn0617/clio/releases/tag/v0.1.0

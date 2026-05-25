# Changelog

All notable changes to clio are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.3.0]: https://github.com/linhn0617/clio/releases/tag/v0.3.0
[0.2.0]: https://github.com/linhn0617/clio/releases/tag/v0.2.0
[0.1.0]: https://github.com/linhn0617/clio/releases/tag/v0.1.0

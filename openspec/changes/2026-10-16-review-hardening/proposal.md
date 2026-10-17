## Why

A six-way review on 2026-10-16 (official Claude Code docs + CHANGELOG 2.1.257–300,
competitor survey, and three independent perf/security/product reviews — reports in
the session scratchpad) found seven small-footprint defects that each conflict with
clio's stated purpose (durable, private, cross-session history for Claude Code):

1. **History silently lost.** Claude Code deletes transcripts after `cleanupPeriodDays`
   (default 30). clio's `PurgeMissing` (`internal/ingest/ingest.go:715`, called from
   `internal/cli/common.go:106`, `internal/cli/mcp.go:185`, `internal/watcher/watcher.go:94`)
   deletes the indexed rows as soon as the source file disappears — so the index never
   outlives the official retention window. On the maintainer's machine the Claude Code
   history starts 2026-08-20 while Codex (not auto-deleted) goes back to 2025-09-21.
2. **FTS bloat.** `index --full` deletes then reinserts every message; FTS5 only tombstones,
   and nothing ever runs `optimize`. Real DB: `messages_fts_data` 2135 MiB vs `messages`
   557 MiB, 21.6 % freelist; `optimize` + `VACUUM` on a copy took 3.66 GB → 0.99 GB.
3. **JSON object keys are not redacted.** `redactWalkDepth` (`internal/ingest/redact.go`)
   only name-checks keys with `isSecretKey`; a key that *is* a token (`{"ghp_…":1}`) or
   carries `<private>…</private>` is stored verbatim.
4. **`clio list` surfaces subagents.** The promote subquery in
   `internal/sessions/sessions.go:82` lets children through; 23 of the top 50 rows on the
   real index are `agent-*` sessions. The same `ListSessions` feeds MCP and recall.
5. **recall digest defects.** On `resume`/`compact` it re-injects the current session;
   the project prefix match (`LIKE prefix%`, `sessions.go:119`) has no `/` boundary so a
   sibling project leaks in; output is not bounded to the hook `additionalContext` limit
   (10,000 chars — over that, Claude Code injects only a path + 2,000-char preview); the
   installed SessionStart hook has no matcher and no timeout.
6. **Terminal escapes pass through.** `show`/`list`/`search`/`ask`/`recall` and the TUI
   print message content verbatim; an indexed transcript can write the clipboard (OSC 52),
   retitle the window (OSC 0) or clear the screen.
7. **install-hook robustness.** The hook command is not quoted (a binary path with a space
   exits 127 and can neither be matched nor uninstalled), and writing `settings.json`
   replaces a symlink with a regular file (`internal/claudeconfig/claudeconfig.go:192,213`).

## What Changes

- **session-ingest**: source files that disappear are no longer purged by default; the
  rows are retained and the `ingest_state` row is marked missing (`source_missing_at`).
  Explicit deletion stays available via `clio index --purge-missing`. JSON object keys go
  through the same string redaction as values.
- **fts-search / diagnostics / raw-json-retention**: `index --full` runs an FTS5
  `optimize` at the end; `doctor` reports FTS bloat; `prune-raw --vacuum` optimizes first.
- **cli-surface / mcp-server**: `ListSessions` hides subagent children whenever their
  parent exists in the index (not only when the parent is on the same page).
- **recall-hook**: recall excludes the session being resumed, matches the project path on a
  `/` boundary, caps output under the 10,000-char hook limit, and the installed hook
  carries a matcher and a timeout.
- **cli-surface / tui**: all user-facing message text is passed through an escape
  stripper before printing.
- **mcp-installer / recall-hook**: hook commands are shell-quoted; config writes preserve a
  symlinked `settings.json`.
- **documentation**: README (en + zh-TW) explain `cleanupPeriodDays` and the retention
  behaviour.

## Non-goals

Regex prefilter / redaction CPU work, MCP connect-before-catch-up, secret pattern
additions, doctor `--quick`, MCP response budgets, Gemini source status, and the
PreToolUse `Bash` matcher are deferred (review items B/C).

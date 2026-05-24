# Design — fix-watcher-deletions

Umbrella design: `docs/superpowers/specs/2026-05-24-clio-quality-security-batch-design.md` (change ③).

## C7 — confirm-then-purge (umbrella decision 1)

Why not act on `Remove`/`Rename` events: fsnotify emits them during the normal
atomic-write pattern (write temp, rename over the target), so a raw event does not mean
the data is gone. The authoritative signal is a filesystem check at reconciliation time.

Add a reconciliation helper in `internal/ingest` (e.g. `PurgeMissing(ctx, projectsDir)`):

0. **Root guard (blast-radius protection, eng-review D1; refined by outside-voice #6).**
   First open/read the `projectsDir` root with `os.ReadDir` (not bare `os.Stat`, which does
   not prove the dir is readable/traversable). If it errors (home not mounted, dir
   moved/renamed, unreadable), SKIP reconciliation entirely and return without purging
   anything. A missing/unreadable root means "filesystem unavailable", never "the user
   deleted every session".
1. Read all distinct `source_file` values from `ingest_state` (and any `sessions` rows
   whose `source_file` has no `ingest_state`).
2. For each, `os.Stat`. If it does not exist (`errors.Is(err, fs.ErrNotExist)`), mark it
   for purge. Purge: `DELETE FROM messages WHERE session_uuid = ?` (delete triggers keep
   `messages_fts` in sync), `DELETE FROM tool_calls WHERE message_id NOT IN (SELECT id
   FROM messages)` (orphans, matching migration 0003's pattern), `DELETE FROM sessions
   WHERE uuid = ?`, `DELETE FROM ingest_state WHERE source_file = ?`. One transaction per
   purged source.
3. Other stat errors (permission, I/O) are NOT treated as "gone" — skip and leave the
   rows; only `ErrNotExist` purges.
4. **Safety cap (eng-review D1; refined by outside-voice #3).** Suppress the purge only
   when the missing set is BOTH a large absolute count AND a large fraction of all known
   sources (e.g. missing > 10 AND missing > 50% of M). A pure ratio breaks small/new
   installs (1/1 or 2/2 legitimate deletions would never purge); requiring both means
   normal small deletions go through, but a mass disappearance (filesystem problem) is
   still caught. On suppression, log a warning and leave the rows.

Call sites:

- MCP watcher backstop (`internal/watcher`): run `PurgeMissing` alongside the existing
  60s `IngestAll`.
- CLI catch-up (the incremental pass each CLI command runs when MCP is not the writer):
  run `PurgeMissing` so deletes are reflected without the MCP server.

Responsiveness is bounded by the backstop period (60s) when MCP runs, or by the next
CLI invocation otherwise. A false purge (file briefly absent) self-heals: the next pass
re-ingests the file from scratch.

uuid↔source_file mapping: purge keys on `source_file`; the `sessions.uuid` for a source
is `sessionUUIDFromPath(source_file)`, and `ingest_state` already keys on `source_file`,
so both tables are addressable without a join.

## C8 — pidAlive treats EPERM as alive

```go
err := signalProc(proc, syscall.Signal(0))
if err == nil {
    return true
}
return errors.Is(err, syscall.EPERM) // exists but not signalable by us
```

Introduce a package seam `var signalProc = func(p *os.Process, s os.Signal) error { return p.Signal(s) }`
so a test can force an `EPERM` return and assert `pidAlive` is true, without depending on
process ownership in CI. PID reuse (a recycled pid pointing at an unrelated process) is a
known limitation of pid-based liveness and is out of scope for this change.

## Codex review outcomes (implementation)

- TOCTOU: `purgeSource` re-stats the source immediately before deleting; if it reappeared
  since the missing-scan, the purge is skipped.
- Ghost rows: deletion keys on `sessionUUIDFromPath(src)` (the canonical uuid), so messages
  / tool_calls / FTS are removed even when the `sessions` row is already gone — not on a
  `sessions` subquery that would miss them.
- Kept single-pass purge (rejected codex's 2-pass / subdir-guard) with reasoning: Claude
  session files are append-only and never atomically rewritten, so an `ErrNotExist` is a
  strong signal of genuine deletion, not a transient window. A subdir-guard would refuse to
  purge a deliberately deleted project directory (the intended behavior), and in-memory
  2-pass confirmation would never fire on the CLI path (a fresh Ingester per command). The
  root guard plus the pre-delete re-stat cover whole-filesystem unavailability. If a
  non-append source type is ever added, revisit with a persisted missing-since confirmation.

## Integration review outcomes (cross-change, codex)

- Moved/renamed source (same filename => same uuid, e.g. a renamed project dir):
  `IngestAll` re-ingests the new path under the same uuid first, so purging the OLD path
  by uuid would clobber the live session. `purgeSource` now only deletes the session/
  messages when this src still owns the uuid (no session row, or its `source_file` is
  empty/equals src); otherwise it removes just the stale `ingest_state` row.
- The MCP leader runs `PurgeMissing` right after startup catch-up and before serving, so
  deletions that happened while clio was down are reflected immediately (not up to one
  60s backstop later), including for CLI readers that defer to the live leader.

## Out of scope

- Reacting to individual `Remove`/`Rename` events for faster purge (decision 1 makes the
  backstop the authority; sub-60s deletion latency is not a requirement).
- Defeating PID reuse (would need richer process identity).

## Test strategy

- `internal/ingest`: ingest two sessions, delete one source file, run `PurgeMissing`,
  assert the deleted session's rows and FTS entries are gone and the other is intact; a
  file that is unreadable (permission) but present is NOT purged.
- `internal/lock`: override `signalProc` to return `EPERM`; assert `pidAlive` is true.

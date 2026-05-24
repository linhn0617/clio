## Why

The code review (internal + codex) found five correctness/UX defects across the CLI,
installer, diagnostics, and MCP surface:

1. **`ResolvePrefix` can drop the exact match.** `WHERE uuid = ? OR uuid LIKE ? LIMIT 2`
   (`sessions.go:86-87`) has no `ORDER BY`, so when ≥3 sessions share a prefix the exact
   row may fall outside the first two and the in-loop exact-match shortcut never sees it —
   an exact uuid then resolves as `ErrAmbiguous`. The prefix `LIKE` is also unescaped, and
   `rows.Err()` is never checked.
2. **`show --format raw` prints duplicate event lines.** One source `.jsonl` line can
   expand into several `messages` rows that share the same `raw_json`; `show.go:47-49`
   prints every row, so the raw dump repeats lines, no longer matching the source file.
3. **`claudeconfig` silently destroys a non-object `mcpServers`.** `serversMap`
   (`claudeconfig.go:54-59`) returns a fresh empty map when `mcpServers` exists but is not
   a JSON object, and `mutate` then overwrites the user's value — data loss in the user's
   primary Claude config.
4. **`doctor` always exits 0 and swallows query errors.** `cli/doctor.go:46` returns `nil`
   even when checks fail, so scripts/CI can't detect an unhealthy index. Several
   `QueryRow(...).Scan(...)` calls in `doctor.go` ignore their error, so a failing query
   reads as a passing/0-count check.
5. **`activity_summary` "day" buckets are UTC.** `date(s.ended_at,'unixepoch')`
   (`sessions.go:163`) groups by UTC day, so late-evening local activity lands in the next
   day's bucket.

## What Changes

- **Modified** `ResolvePrefix`: an exact `uuid = ?` lookup first (wins regardless of prefix
  collisions); only on no exact match, an escaped prefix `LIKE ? ESCAPE '\' LIMIT 2`; check
  `rows.Err()`.
- **Modified** `show --format raw`: skip an event line whose `raw_json` equals the
  immediately preceding line, so the raw dump matches the source.
- **Modified** `claudeconfig`: when `mcpServers` is present but not a JSON object, return an
  error and leave `~/.claude.json` untouched (no overwrite).
- **Modified** `doctor`: exit non-zero when any check fails; capture `Scan`/`rows.Err`
  errors and mark the affected check failed instead of silently passing.
- **Modified** `activity_summary` day grouping to local day
  (`date(...,'unixepoch','localtime')`).

## Capabilities

### Modified Capabilities

- `cli-surface`: exact-id-wins prefix resolution and de-duplicated `--format raw` output.
- `mcp-installer`: reject a non-object `mcpServers` rather than overwriting it.
- `diagnostics`: non-zero exit on failure and no silently-swallowed query errors.
- `mcp-server`: `activity_summary` day grouping uses the local day.

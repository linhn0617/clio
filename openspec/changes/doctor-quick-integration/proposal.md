## Why

`clio doctor` took 67 seconds on a 968 MB index. The time goes to three whole-database scans:
`PRAGMA integrity_check`, the FTS5 `integrity-check`, and the `dbstat` bloat measurement. For the
everyday question "is the index alive?" that is far too slow. Separately, doctor looks only at the
index, while the failures users hit most after a reinstall are outside it: a hook that points at a
binary that no longer exists, an MCP entry that was never registered, a lease left behind by a dead
server, and an index nobody has refreshed for days.

## What Changes

- **diagnostics**: `clio doctor --quick` skips the whole-database scans: `db integrity` and
  `fts bloat` print as `[skip] … skipped (--quick)`, and the `fts index` line still compares row
  counts but notes `content check skipped (--quick)`. A quick run lists the same check names as a
  full one. Skips never fail the run. (`quick_check` was tried first and still walks every page:
  about half of a quick run on a 1.2 GB index.)
- **diagnostics**: four read-only integration checks in both modes: `hook binary` (recall and
  file-history judged separately: missing hook, unrunnable unquoted path, or the older `Read`
  matcher all ask for `clio install-hook`), `mcp registration` (not registered is ok, MCP is
  optional; a bare command is noted, not judged on doctor's PATH), `mcp lease`, `index freshness`
  (judges the age of the oldest unindexed change that source reconciliation found, warning past
  24 h; a partial last line is not a change; a quiet index is fine). The lease check separates a
  live-but-late leader from a dead one.
- Not done (`debt:` in code): comparing the running MCP server's version with this binary; it
  needs the lease file to carry a version.
- README and README_zh-TW mention `--quick` and the integration checks.

## Impact

- Default `clio doctor` gains four lines and one new failure class (the integration warnings), so
  its exit status can now be non-zero for a stale hook path or an index older than a day.
- `internal/lock` gains a read-only `Inspect` (`IsHeld` now delegates to it; per-OS `pidAlive`);
  `internal/claudeconfig` gains read-only `ClioHookCommands` (returning `HookEntry` values with the
  unquoted and older-matcher flags) and `LookupServer`, and its hook recognisers accept `clio.exe`.

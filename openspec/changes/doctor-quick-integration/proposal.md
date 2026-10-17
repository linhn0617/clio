## Why

`clio doctor` took 67 seconds on a 968 MB index. The time goes to three whole-database scans:
`PRAGMA integrity_check`, the FTS5 `integrity-check`, and the `dbstat` bloat measurement. For the
everyday question "is the index alive?" that is far too slow. Separately, doctor looks only at the
index, while the failures users hit most after a reinstall are outside it: a hook that points at a
binary that no longer exists, an MCP entry that was never registered, a lease left behind by a dead
server, and an index nobody has refreshed for days.

## What Changes

- **diagnostics**: `clio doctor --quick` skips all integrity pragmas and FTS checks (`db integrity`,
  the FTS content check, the bloat measurement), printing each as `[skip] … skipped (--quick)`.
  Skips never fail the run. (`quick_check` was tried first and still walks every page: about half
  of a quick run on a 1.2 GB index.)
- **diagnostics**: four read-only integration checks in both modes: `hook binary`,
  `mcp registration` (not registered is ok, MCP is optional), `mcp lease`, `index freshness`
  (warns only when tracked files hold unabsorbed changes older than 24 h, or nothing was ever
  indexed; a quiet index is fine). The lease check separates a live-but-late leader from a dead one.
- Not done (`debt:` in code): comparing the running MCP server's version with this binary; it
  needs the lease file to carry a version.
- README and README_zh-TW mention `--quick` and the integration checks.

## Impact

- Default `clio doctor` gains four lines and one new failure class (the integration warnings), so
  its exit status can now be non-zero for a stale hook path or an index older than a day.
- `internal/lock` gains a read-only `Inspect` (`IsHeld` now delegates to it; per-OS `pidAlive`);
  `internal/claudeconfig` gains read-only `ClioHookCommands` and `LookupServer`, and its hook
  recognisers accept `clio.exe`.

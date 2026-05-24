## Why

The review's P4 list: the remaining verified findings after the security and
data-correctness changes (①–③). A mix of one cross-process safety bug, two small
observable hardening items, and several internal-quality changes with no behavior
change. Grouped into one batch since each is isolated and low-risk, matching the
`polish-review-p3` precedent.

## What Changes

- **Modified** `claudeconfig` mutation to serialize across processes with a lock, so two
  concurrent `install-mcp`/`uninstall-mcp` runs (or one plus another tool editing
  `~/.claude.json`) cannot lost-update each other. The atomic rename already prevents
  corruption; this prevents a dropped edit.
- **Modified** `clio doctor` to check that the database file and its `-wal`/`-shm`
  sidecars (which hold indexed content) are `0600`, reporting any that are not.
- **Modified** the MCP `activity_summary` tool to validate `group_by` at the request
  boundary and return a clear tool error for an unsupported value.
- Internal (no behavior change): thread `context.Context` through the data layer
  (`QueryContext`/`ExecContext`) and from the MCP handlers and CLI (idiom hygiene and
  groundwork — this alone does not make a cancelled request return promptly while the
  synchronous catch-up runs; bounding that catch-up is a follow-up); add a `cache_size`
  pragma and a one-time post-migration
  `ANALYZE`; enforce `0600` on the `-wal`/`-shm` sidecars after open; tune the search
  re-rank `overscan`; check the `json.Marshal` error in `AddServer`. Add a benchmark for
  the initial full index and adopt prepared statements / batched inserts ONLY if it
  shows a meaningful gain — otherwise record the measurement and drop it.

## Capabilities

### Modified Capabilities

- `mcp-installer`: config mutation is serialized across processes.
- `diagnostics`: `doctor` checks file permissions on the DB (and sidecars), config, and
  lock files.
- `mcp-server`: `activity_summary` validates `group_by` at the boundary.

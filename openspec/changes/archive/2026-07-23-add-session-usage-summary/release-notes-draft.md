# Release notes draft (for the version shipping add-session-usage-summary)

## Session-level token usage (`clio usage`)

- New `clio usage` command: top sessions/projects/models by token usage,
  sectioned per source (token counts are not comparable across tools — no
  cross-source totals). `--quota` shows last-observed rate-limit snapshots
  (Codex persists these) with mandatory staleness rendering. Raw token counts
  only; no monetary amounts anywhere.
- `clio list` and the TUI browse tab show per-session token totals.
- MCP `activity_summary` gains `group_by: "usage"` (token aggregates only;
  quota data is CLI-only and never crosses MCP).
- `clio doctor` reports usage coverage and diagnostics.

**Action required for existing indexes:** usage for sessions indexed before
this version is backfilled by a one-time `clio index --full` (stop Claude
Code / the clio MCP server first — a locked `--full` now refuses loudly
instead of reporting success).

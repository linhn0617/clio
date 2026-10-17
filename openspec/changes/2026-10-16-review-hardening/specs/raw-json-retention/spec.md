## MODIFIED Requirements

### Requirement: space is reclaimed on VACUUM; --vacuum is lock-checked first
The command SHALL reclaim file space only via VACUUM (blanking `raw_json` moves
freed pages to the freelist; the file shrinks only on VACUUM). When `--vacuum`
is requested the command SHALL check the MCP index lock **before** any mutation
and, if held, exit non-zero making NO database change (no prune, no vacuum).
With the lock free it SHALL prune, then run the FTS5 `optimize` command on
`messages_fts` (so tombstoned FTS pages are free for VACUUM to return; a failure
there SHALL only warn and continue), then VACUUM. Without `--vacuum` it SHALL prune
and print the exact logical raw bytes removed
(`SUM(length(CAST(raw_json AS BLOB)))` — true UTF-8 bytes — over the pruned
rows, computed before blanking) and how to VACUUM to reclaim on disk.

#### Scenario: Vacuum after prune shrinks the file
- **WHEN** `clio prune-raw --older-than 30d --vacuum` runs with the index lock
  free
- **THEN** the file size decreases and no FTS/content/usage data changes

#### Scenario: Vacuum optimizes the FTS index first
- **WHEN** `clio prune-raw --older-than 30d --vacuum` prunes at least one message
- **THEN** it SHALL print `fts optimized` before `VACUUM done`, and an optimize failure
  SHALL be a warning that does not skip the VACUUM

## Why

`clio prune-raw` blanks `messages.raw_json` for old, restorable sessions, and `clio
index --full` deliberately restores it. On 2026-11-01 the maintainer's DB was 3.0 GB;
`prune-raw --older-than 14d --vacuum` brought it to 906 MB, and the next `--full`
(which releases with new parser behaviour require) wrote the old raw_json back. The
user has to prune by hand after every full reindex. Design:
`docs/superpowers/specs/2026-11-07-raw-retention-design.md`.

## What Changes

- **raw-json-retention**: a rolling retention window stored in the index
  (`settings` table, migration 0017) and set with `clio config raw-retention
  [VALUE|off]`. Values use prune-raw's relative grammar (`14d`, `12h`, `30m`); a date,
  zero, a negative value or garbage is rejected. Unset means today's behaviour.
- Every ingest path (`clio index`, `--full`, MCP catch-up, watcher, `install-mcp`)
  stores raw_json as `''` for the rows a file commit inserts when the session's
  `COALESCE(ended_at, started_at)` after that commit is older than `now - window`
  (cutoff computed once per run). A session with no timestamp at all keeps raw_json
  (decided 2026-11-07; manual prune-raw still ages it as 0). Content, FTS, tool calls/targets and usage are
  written as today.
- `clio index` (incremental and `--full`) ends with one prune pass that reuses
  prune-raw's preflight and in-transaction revalidation, for sessions that aged past
  the window after they were written. The MCP leader runs the same pass once at the end
  of its startup catch-up (a failure is only logged). The per-read catch-ups (MCP and
  CLI), the watcher and `install-mcp` do not sweep.
- `clio index --full`'s residual check no longer reports sessions the active window
  keeps blank. Restore path: `clio config raw-retention off`, then `clio index --full`.
- An unreadable setting or a stored value that no longer parses is treated as off
  (raw kept); ingest logs a warning naming the value, `clio doctor` warns.
- **diagnostics**: `clio doctor` prints a `raw retention` line next to `pruned raw_json`.
- **cli-surface**: the `show --format raw` pruned note names the retention window and
  its restore path when one is set.
- **documentation**: both READMEs and `docs/USAGE.md` document `clio config
  raw-retention` and the restore path.
- Unchanged: no automatic VACUUM; `quota_snapshots.raw_json` is untouched.

## Impact

- New: `internal/db/migrations/0017_settings.sql`,
  `internal/db/migrations/0018_backfill_session_ended_at.sql` (backfills `ended_at`
  the old incremental upsert cleared; rewrites existing rows once),
  `internal/db/settings.go`, `internal/cli/config.go`.
- Changed: `internal/timeutil/timeutil.go`, `internal/ingest/ingest.go`,
  `internal/cli/{index,prune,show,root,mcp}.go`, `internal/doctor/doctor.go`, both READMEs,
  `docs/USAGE.md`.

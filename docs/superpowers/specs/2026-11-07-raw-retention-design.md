# clio: raw_json retention policy — Design

- **Date:** 2026-11-07
- **Status:** Approved in brainstorming (options chosen by the user: B = retention policy, 1 = stored in the DB, 丙 = drop at write time + sweep at the end of `clio index`)
- **Builds on:** `clio prune-raw` (migration 0012, `internal/cli/prune.go`, `internal/sessions/prune.go`), the `--full` restore path and `checkPrunedResiduals` (`internal/cli/index.go`)

## 1. Problem

`clio prune-raw` blanks `messages.raw_json` for old, restorable sessions, and `clio index --full` deliberately restores it, because a full rebuild DELETEs and re-INSERTs every message. Real numbers from 2026-11-01: the DB was 3.0 GB, and `prune-raw --older-than 14d --vacuum` brought it to 906 MB (248 MB of raw_json, plus pages freed by FTS optimize). The next `--full` writes the old raw_json back, so the user has to prune by hand again after every full reindex. Releases that need a reindex (new parser behaviour) trigger exactly that.

`raw_json` is read only by `clio show --format raw|json` and by the activity repair pass. Search, content, usage (which re-reads the source file), and activity facts written at ingest do not need it.

## 2. Goals / non-goals

**Goals**
- The user sets a rolling retention once (for example `14d`). From then on, raw_json for sessions whose last activity is older than the window is not kept, and `--full` does not write it back.
- No extra work on the MCP per-call ingest path beyond a per-file decision.
- A clear, documented way to restore raw_json.

**Non-goals**
- No automatic VACUUM. It needs exclusive access, which conflicts with `clio mcp`. `prune-raw --vacuum` stays the way to shrink the file.
- No config file, no environment variable.
- `quota_snapshots.raw_json` is untouched.

## 3. Design

### 3.1 Setting

- New migration: a key/value table `settings(key TEXT PRIMARY KEY, value TEXT NOT NULL)`.
- New command `clio config raw-retention [VALUE]`:
  - With no VALUE, prints the current value, or `off`.
  - `off` deletes the key.
  - Any other value must be a relative duration accepted by the same parser `prune-raw` uses (`14d`, `12h`). An absolute date (`YYYY-MM-DD`), zero, a negative value, or garbage is rejected with the same wording style as `prune-raw`. The window rolls, so an absolute date makes no sense.
- Default: unset. With the key unset, behaviour is identical to today.
- Each ingest/index run computes `cutoff = now - retention` once, at its start.

### 3.2 Drop at write time (every ingest path: `clio index`, `--full`, MCP, watch, install-mcp)

- When a file's batch is committed, compute the session's last activity time as `COALESCE(ended_at, started_at)` after this commit. If it is older than the cutoff, the rows inserted in this commit store `raw_json = ''`. Everything else (content, FTS, tool calls/targets, usage) is written as today.
- A session with no timestamps at all (both columns NULL) has no known age and is never treated as outside the window: its raw_json is kept (decided 2026-11-07, replacing the earlier choice of `prune-raw`'s `COALESCE(ended_at, started_at, 0)`, which made such a session the oldest and blanked it at write time). The same rule holds in the sweep (§3.3) and the residual check (§3.4). Manual `clio prune-raw --older-than` keeps its existing behaviour and still ages an undated session as 0.
- Applies to every source (claude-code, codex), the same as `prune-raw`.
- Existing data repair (decided 2026-11-07): before the incremental `upsertSession` fix, a batch without a timestamp stored `ended_at = NULL` for a dated session, which this rule would then age by `started_at`. Migration 0018 rewrites those rows once: `ended_at` becomes the session's newest message `ts` where `ended_at` is NULL and some message is timestamped; every other row is untouched, and a re-run changes nothing. The value is a lower bound when the newest timestamped line produced no message (mostly Codex `event_msg`).
- The source file was just read successfully, so the raw form is restorable at that moment, which is the same precondition `prune-raw` checks.
- A resumed old session: its last activity becomes recent, so the new rows keep raw_json. Rows blanked earlier stay blank until a `--full` run with retention off (or a longer window).
- **To verify during implementation:** activity facts (`tool_calls`, `tool_targets`) must be written from the parsed events, not read back from stored raw_json. `RepairActivity` reads stored raw_json, but it already tolerates rows blanked by `prune-raw`. A test pins this down.

### 3.3 Sweep at the end of `clio index` and at MCP startup

- When retention is set, `clio index` (incremental and `--full`) runs one prune pass before it exits. It reuses the `prune-raw` machinery: candidate selection, `classifyPrunable` restorability checks, and the in-transaction revalidation. It covers sessions that aged past the window since they were written. Two differences from `prune-raw`: candidates are only sessions that still hold some raw_json (one already blanked has nothing left, and re-stat-ing every old transcript on each run is waste), and an undated session is neither a candidate nor accepted by the revalidation (§3.2, decided 2026-11-07).
- Sessions skipped by those checks (missing, changed, lagged, unverified, pending backfill) are left intact, as today.
- The sweep also runs once in the MCP leader's startup catch-up (decided 2026-11-07). Without it, a user who only uses MCP never prunes sessions that aged after being written; once Claude Code deletes the transcript (default 30 days), they are "missing" and stay unprunable forever. A sweep failure there is logged and does not stop the server. The catch-up that runs before each MCP tool call does not sweep, and neither do watch and install-mcp. Those paths get only §3.2.
- It prints one summary line when it pruned something. Skips are not warnings.

### 3.4 Restore path and the residual check

- Restore: `clio config raw-retention off` and then `clio index --full`. The help text of `prune-raw`, `index --full`, and both READMEs states this.
- `checkPrunedResiduals` (after `--full`) excludes sessions that the active retention says must be blank, so they are not reported as "could not be restored". An undated session is never one of them (§3.2, decided 2026-11-07): if it is blank, it is reported.

### 3.5 Visibility

- `clio doctor` prints the retention setting (`raw retention: off` or `14d`) next to the existing "pruned raw_json" line.
- `clio show --format raw|json` keeps its existing pruned note. The note's restore hint mentions the retention setting when one is set.

## 4. Error handling

- Settings table unreadable, or a stored value that no longer parses: ingest proceeds with retention treated as **off** (fail open, so raw is kept and no data is lost), and `clio index` and `clio doctor` print a warning naming the bad value.
- A sweep failure does not fail the ingest that already committed: `clio index` prints the error and exits non-zero, the same as other post-ingest checks.

## 5. Testing (TDD, red first)

- Setting: get / set / off round-trip. Rejects `0d`, `-5d`, `2026-10-01`, `abc`. Unset means today's behaviour.
- `--full` with retention: an old session's rows get `raw_json = ''`, while content, FTS hits, tool calls and targets are present. A recent session keeps raw.
- Incremental ingest of a new old file blanks raw. Appending to an old session makes it recent, so the new rows keep raw.
- Sweep: a session that aged past the cutoff is pruned. A session whose source changed or went missing is skipped.
- Restore: retention off plus `--full` restores raw_json for every session whose file exists.
- `checkPrunedResiduals` does not report retention-blanked sessions. It still reports a genuinely unrestorable one when retention is off.
- MCP path: the startup catch-up sweeps once; the per-call catch-up does not sweep. A sweep failure at startup is logged and does not stop the server.
- Codex: a rollout older than the window is ingested with blank raw_json.
- Bad stored value: ingest keeps raw, and a warning is printed.

## 6. Workflow

openspec change (`validate --strict`), then TDD, then codex review of the real diff, then PR, then merge after user approval, then a release PR (archive, CHANGELOG, version bump in both READMEs).

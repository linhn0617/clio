## ADDED Requirements

### Requirement: Rolling raw_json retention setting
The index SHALL store an optional raw_json retention window in a `settings`
key/value table (key `raw_retention`). `clio config raw-retention` with no value SHALL
print the stored window, or `off` when unset. `clio config raw-retention off` SHALL
delete the key. Any other value SHALL be a positive relative duration in prune-raw's
grammar (`14d`, `12h`, `30m`); an absolute date, zero, a negative value, or garbage
SHALL be rejected with a non-zero exit and nothing stored. With the key unset, ingest
and `clio index` SHALL behave exactly as without this feature.

#### Scenario: Set, read, and turn off
- **WHEN** the user runs `clio config raw-retention 14d`, then `clio config
  raw-retention`, then `clio config raw-retention off`, then `clio config raw-retention`
- **THEN** the second command prints `14d` and the last prints `off`

#### Scenario: Invalid values are rejected
- **WHEN** the user runs `clio config raw-retention` with `0d`, `-5d`, `2026-10-01`, or
  `abc`
- **THEN** each exits non-zero with an error saying the value must be a positive
  duration, and the stored setting is unchanged

### Requirement: Retention drops raw_json at write time
When a window is set, each ingest run SHALL compute `cutoff = now - window` once at
its start. When a file's batch is committed, the rows inserted by that commit SHALL
store `raw_json = ''` if the session's `COALESCE(ended_at, started_at)` after the
commit is older than the cutoff. A session with neither timestamp has no known age and
SHALL keep raw_json. Content, FTS, tool calls, tool targets, and usage
SHALL be written as without retention, from the parsed events. Rows written by earlier
commits SHALL NOT be changed by this rule. An incremental commit whose new lines carry
no timestamp SHALL leave the session's stored `ended_at` unchanged (NULL stays NULL),
so such a batch never makes a live session look older.

#### Scenario: Full reindex with retention
- **WHEN** `clio index --full` runs with retention `14d` over an old session and a
  session active today
- **THEN** every row of the old session has `raw_json = ''` while its content, FTS hits,
  tool calls and tool targets are present, and the recent session keeps raw_json

#### Scenario: Undated session keeps raw_json
- **WHEN** retention is `14d` and a session whose lines carry no timestamp (both
  `started_at` and `ended_at` NULL) is ingested, incrementally or with `--full`
- **THEN** its rows keep raw_json

#### Scenario: Appending to an old session
- **WHEN** an old, already-blanked session file receives a new line stamped now and is
  ingested incrementally
- **THEN** the new row keeps raw_json and the earlier rows stay blank

#### Scenario: Appending untimestamped lines to a live session
- **WHEN** retention is `14d`, a session that started more than 14 days ago is active
  today, and a line without a timestamp (a `summary` record or a user line) is appended
  and `clio index` runs
- **THEN** its `ended_at` is unchanged, the new row keeps raw_json, and the sweep does not
  prune the session

### Requirement: clio index sweeps sessions that aged past the window
When a window is set, `clio index` (incremental and `--full`) SHALL, after ingest,
blank raw_json of sessions older than the run's cutoff using prune-raw's preflight
(candidate selection, restorability checks, pending-backfill exclusion) and its
in-transaction revalidation, with two differences: only sessions that still hold some
raw_json SHALL be candidates, and a session with neither `ended_at` nor `started_at`
SHALL NOT be treated as older than the cutoff by the candidate selection or the
revalidation. Manual `clio prune-raw` SHALL keep its candidate set and its age rule
(an undated session ages as 0). Skipped sessions SHALL be left intact without a warning.
It SHALL print one summary line only when it pruned something. A sweep failure SHALL
NOT roll back the committed ingest; the command SHALL report it and exit non-zero.
The MCP leader SHALL run the same sweep once, at the end of its startup catch-up; a
failure there SHALL only be logged and SHALL NOT stop the server from starting.

#### Scenario: A session ages past the window
- **WHEN** a session written with raw_json is older than the window and `clio index`
  runs
- **THEN** its raw_json is blanked and a `raw retention` summary line is printed

#### Scenario: MCP leader startup sweeps once
- **WHEN** retention is set, an aged session written with raw_json is unchanged on disk,
  and `clio mcp` starts as leader
- **THEN** its startup catch-up blanks that session's raw_json

#### Scenario: MCP startup sweep failure
- **WHEN** the sweep fails during the MCP leader's startup catch-up
- **THEN** the failure is logged and the leader still starts serving as leader

#### Scenario: Undated session is not swept
- **WHEN** retention is set and a session with raw_json has neither `ended_at` nor
  `started_at`, and `clio index` runs
- **THEN** its raw_json is kept

#### Scenario: Already-blanked sessions are not re-examined
- **WHEN** retention is set and an aged session's raw_json was already blanked
- **THEN** the sweep does not stat its transcript, while `clio prune-raw` still counts
  it among its candidates

#### Scenario: A session whose transcript is missing
- **WHEN** the aged session's source file no longer exists and `clio index` runs
- **THEN** its raw_json is kept and no retention line is printed

### Requirement: Retention fails open on an unreadable or invalid setting
Ingest SHALL treat retention as off (raw_json kept) when the settings table cannot be
read or the stored value no longer parses, and SHALL log a warning naming the stored
value; `clio index` SHALL therefore print that warning, and SHALL NOT sweep.

#### Scenario: Stored value is a date
- **WHEN** the stored value is `2026-10-01` and an ingest runs
- **THEN** raw_json is kept and a warning naming `2026-10-01` is logged

## MODIFIED Requirements

### Requirement: pruned raw_json is restorable and restore failure is visible
Pruning SHALL be reversible for every eligible (restorable) session: a
subsequent `clio index --full` re-derives and re-stores `raw_json` from the
source file (Claude re-parse, Codex rollout). With a raw retention window set,
`--full` keeps sessions older than the window blank; the restore path is
`clio config raw-retention off` followed by `clio index --full`, and the help text
of `prune-raw` and `index --full` SHALL say so. Because
full reindex logs per-file failures and continues, `clio index --full` SHALL,
after a full run, report and exit non-zero if any session still has pruned
`raw_json`, so an unrestored prune is never silent. A session whose source file
the full run skipped as still changing is excepted: the run names that file
instead, and the next full reindex restores it. A session older than the run's
retention cutoff is also excepted, because the window keeps it blank on purpose; a
session with neither `ended_at` nor `started_at` is never excepted this way.
The prune command SHALL print the restore path on completion, and with a window set
that path SHALL be `clio config raw-retention off` followed by `clio index --full`.

#### Scenario: Full reindex restores pruned raw_json for both sources
- **WHEN** Claude and Codex sessions are each pruned and then `clio
  index --full` runs with their source files present
- **THEN** each session's `raw_json` is repopulated

#### Scenario: Unrestored prune is surfaced
- **WHEN** after `clio index --full` some pruned session was not restored (e.g.
  its file became unreadable)
- **THEN** the command reports that session and exits non-zero

#### Scenario: Pruned session whose file is still changing
- **WHEN** after `clio index --full` a pruned session was not restored only
  because its source file kept changing during the run
- **THEN** the command names that file as still changing, does not report the
  session as unrestored, and exits successfully

#### Scenario: Session blank by retention is not a residual
- **WHEN** `clio index --full` runs with retention `14d` and a session older than 14
  days ends with blank raw_json
- **THEN** the command does not report it and exits successfully, while a pruned
  session inside the window is still reported

#### Scenario: Blank undated session is still a residual
- **WHEN** `clio index --full` runs with retention set and a session with neither
  `ended_at` nor `started_at` still has blank raw_json
- **THEN** the command reports it and exits non-zero

#### Scenario: Retention off restores
- **WHEN** the user runs `clio config raw-retention off` and then `clio index --full`
- **THEN** raw_json is restored for every session whose source file exists

#### Scenario: prune-raw restore hint with retention set
- **WHEN** retention is `14d` and `clio prune-raw --older-than 14d` prunes a session
- **THEN** its closing hint names `clio config raw-retention off`, then
  `clio index --full`, and with retention off the hint stays `clio index --full`

### Requirement: space is reclaimed on VACUUM; --vacuum is lock-checked first
The command SHALL reclaim file space only via VACUUM (blanking `raw_json` moves
freed pages to the freelist; the file shrinks only on VACUUM). When `--vacuum`
is requested the command SHALL check the MCP index lock **before** any mutation
and, if held, exit non-zero making NO database change (no prune, no vacuum).
With the lock free it SHALL prune, then run the FTS5 `optimize` command on
`messages_fts` (so tombstoned FTS pages are free for VACUUM to return; a failure
there SHALL only warn and continue), then VACUUM — also when it pruned nothing,
since with a raw retention window set `clio index` has already blanked old sessions.
Without `--vacuum` it SHALL prune
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

#### Scenario: Vacuum runs when nothing is left to prune
- **WHEN** retention is set, `clio index` has already blanked the old sessions, and
  `clio prune-raw --older-than 14d --vacuum` runs with the index lock free
- **THEN** it prints `pruned 0 messages` and `VACUUM done`, and the freelist is empty
  afterwards

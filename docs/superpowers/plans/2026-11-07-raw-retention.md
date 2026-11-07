# raw_json retention policy — Implementation Plan

> For the implementing agent: use superpowers:subagent-driven-development or superpowers:executing-plans, one Task at a time, in order. Every step is complete; copy the code as given. Design: `docs/superpowers/specs/2026-11-07-raw-retention-design.md` (§ numbers below refer to it).

**Goal:** Let the user set a rolling raw_json retention window once (`clio config raw-retention 14d`). From then on every ingest stores old sessions without raw_json, `clio index` prunes sessions that aged past the window with prune-raw's safety checks, and `clio index --full` no longer writes old raw_json back.

**Architecture:** The window is a row in a new `settings` table (migration 0017), read through `db.(*DB).RawRetention`, which parses it with a new `timeutil.ParseRetention` that shares `ParseSince`'s relative grammar. The Ingester reads the setting itself once per `IngestAll` / `IngestFile` call, so all four ingest call sites get the write-time drop without code changes there. Inside each file's commit transaction, after `upsertSession`, a single UPDATE blanks raw_json of the rows that commit inserted when the session's `COALESCE(ended_at, started_at, 0)` is older than the cutoff. `IngestAll` reports the cutoff in `Stats`. The `clio index` command uses it to run a sweep (prune-raw's preflight, extracted from `runPruneRaw` into `pruneEligible`, plus `sessions.PruneRawJSON`) and to exclude retention-blanked sessions from `checkPrunedResiduals`; the MCP leader runs the same sweep once at the end of its startup catch-up (failures only logged). The catch-ups that run before reads never sweep.

**Tech Stack:** Go 1.27 (local `go version go1.27.3`), modernc.org/sqlite (WAL, FTS5 trigram), cobra, log/slog, openspec 1.2.0.

**Global Constraints:**
- Fail open everywhere (§4): an unreadable table or an unparseable stored value means retention off, raw kept, plus a warning naming the value.
- Same age expression as prune-raw: `COALESCE(ended_at, started_at, 0) < cutoff` (`internal/sessions/prune.go:31` and `:154`). The design text writes the two-argument form; the code uses three arguments, and that is what this plan uses (已裁決 #5).
- Do not touch `quota_snapshots.raw_json`; no automatic VACUUM; no config file or environment variable.
- `prune-raw`'s observable behaviour must not change: the extraction in Task 5 is pinned by the existing `go test ./internal/cli/ -run 'TestPrune' -count=1` (24 tests).
- Test command convention: `go test ./internal/<pkg>/ -run <Name> -count=1`. After each Task's green step also run the whole touched package(s) with `-count=1`.
- Tests write SQL that renames tables instead of dropping them (`ALTER TABLE … RENAME TO …`); keep it that way.
- The user's rules apply to every "Commit" step: run `gofmt -l internal` (must print nothing) and `go vet ./...`, run the harness `code-review` skill on code changes, show the diff, and commit only after the user approves. Never push.
- openspec change name: `raw-json-retention-policy`. Tick its `tasks.md` items in the Task that implements them.

---

## Code facts this plan relies on (each read in the current tree)

1. **Migrations.** `internal/db/db.go:20-21` embeds `migrations/*.sql`; `migrate()` (`db.go:108-142`) creates `schema_migrations`, sorts file names lexically (`:123`) and applies each unapplied one; `applyMigration` (`db.go:201-222`) runs it in an IMMEDIATE transaction and records the file name. `Open` (`db.go:37-88`) pings, sets one connection, chmods, then calls `migrate` (`:72`); `OpenReadOnly` (`db.go:92-103`) does **not** migrate. Naming is `NNNN_snake_case.sql`; the newest is `0016_activity_backfill_watermark.sql`, so the new file is `0017_settings.sql`. Migration tests re-run `d.migrate()` after deleting a marker (`internal/db/db_test.go:223-263`).
2. **Ingest commit path.** `commit` (`internal/ingest/ingest.go:512-658`) opens the transaction (`:513`), re-stats the file (`:528-534`), on `changeFull` deletes the session's tool_calls/targets/messages (`:554-564`), INSERTs messages with `INSERT OR IGNORE … raw_json` (`:568-569`), writes tool_calls/targets from `m.ToolCalls`/`m.Targets` (`:584-599`), counts user turns (`:640-643`), calls `upsertSession` (`:645`), `upsertIngestState` (`:649-652`), commits (`:654`). `upsertSession` (`:714-746`) is where `started_at`/`ended_at` change: full = replace both (`:716-721`), incremental = `ended_at = MAX(COALESCE(ended_at,0), ?)` (`:727-737`) plus an `INSERT OR IGNORE` (`:742-745`). So the post-commit `COALESCE(ended_at, started_at, 0)` exists only after `:645`. `messages.id` is `INTEGER PRIMARY KEY AUTOINCREMENT` (`internal/db/migrations/0001_init.sql:16`), so ids inserted by this transaction are exactly `id >= first id this commit inserted`. The `messages_au` trigger fires only `AFTER UPDATE OF content` (`internal/db/migrations/0012_prune_raw_support.sql:6`), so a raw_json-only UPDATE leaves FTS alone (pinned by `TestMigration0012TriggerScopedToContent`, `internal/ingest/prune_support_test.go:11`).
   **Decision: UPDATE before commit, not `''` at INSERT.** Writing `''` at INSERT needs the post-commit age before the rows go in, which means re-implementing `upsertSession`'s rules in Go (full replaces both columns; incremental takes `MAX(COALESCE(ended_at,0), ?)`, which in SQLite yields NULL when the new `EndedAt` is 0, then `COALESCE` falls back to `started_at`). A second copy of those rules would drift silently. Reading the row back after `upsertSession` gives exactly the expression prune-raw compares, and the UPDATE is cheap on FTS because of the 0012 trigger. Cost: blanked rows are written with raw_json and overwritten inside the same transaction (measured on a copy of the real DB in Task 8 Step 5; 已裁決 #1). Recent sessions pay one primary-key SELECT per file commit and no UPDATE.
3. **Activity facts.** Written at ingest from the parsed events (`ingest.go:589-598`, filled by the parser at `internal/ingest/parser.go:97-98` and `internal/ingest/codex.go:123-124`), never read back from stored raw_json. `RepairActivity`/`BackfillActivity` (`internal/ingest/activity.go:92-100`) → `backfillActivity` (`activity.go:102-225`) reads stored `raw_json` (`:148-152`); for an empty string `toolUseBlocks("")` fails `json.Unmarshal` and returns nil (`activity.go:249-253`), and the loop `continue`s (`:201-203`), so blank rows are skipped without error and the watermark still advances (`:219-223`). Task 4 pins this with `TestRawRetentionActivityFromParseAndRepairTolerates`.
4. **`timeutil.ParseSince`** (`internal/timeutil/timeutil.go:15-38`): `func ParseSince(s string) (int64, error)`. `"14d"` → `time.Now().Add(-14*24h).Unix()` (`:20-30`); `"2026-10-01"` → that local midnight's unix time (`:32-35`); `""` → 0; `"-5d"` → a future time; `"0d"` → now; anything else → `invalid time %q (use 7d, 12h, 30m, or YYYY-MM-DD)`. The return value does not say which branch matched, so absolute dates cannot be told apart afterwards. Task 2 extracts the relative branch into `parseRelative` (used by `ParseSince` with identical arithmetic) and adds `ParseRetention`, which accepts only that branch with a count > 0.
5. **prune machinery.** `sessions.PruneCandidates` (`internal/sessions/prune.go:30-58`), `sessions.PendingBackfillSessions` (`:62-82`), `sessions.PruneRawJSON` (`:113-188`, in-transaction revalidation `:151-165`), `classifyPrunable` (`internal/cli/prune.go:168-199`), `underAnyRoot` (`prune.go:201-212`), `runPruneRaw` (`prune.go:78-162`, preflight loop `:113-130`). The sweep runs only from the `clio index` command, which lives in the same package `cli`, so `classifyPrunable`/`underAnyRoot` stay where they are. Task 5 moves only the preflight loop (`prune.go:113-130`) into a new `pruneEligible` that `runPruneRaw` calls; `runPruneRaw` keeps its roots, PruneRawJSON call, summary, and vacuum code unchanged, and the 24 existing `TestPrune*` tests pin the behaviour.
6. **Residual check.** `checkPrunedResiduals` (`internal/cli/index.go:126-154`) lists `sessions.PrunedResiduals` (`internal/sessions/prune.go:193-208`, query `SELECT DISTINCT session_uuid FROM messages WHERE raw_json = ''`), then per uuid reads `SELECT source_file FROM sessions WHERE uuid = ?` (`index.go:138`) to skip still-changing files. Task 6 widens that per-uuid read to also return `COALESCE(ended_at, started_at, 0)` and skips sessions older than the run's cutoff. Existing callers: `index.go:98` and the test `internal/cli/index_settle_test.go:61,69`.
7. **CLI registration.** `newRootCmd` (`internal/cli/root.go:16-46`) lists subcommands in one `root.AddCommand(...)` (`:26-44`). Task 3 adds `newConfigCmd()` after `newPruneRawCmd()` (`:33`).
8. **Ingest call sites.** `internal/cli/common.go:115` (`runIngestBackfill` → `IngestAll`), `internal/cli/mcp.go:181` (read catch-up) and `:351` (`startupCatchup`), `internal/watcher/watcher.go:109` (`backstop` → `IngestAll`) and `:81` (per-event `IngestFile`, not in the brief's list), `internal/cli/install_mcp.go:48`, plus `cmd/clio-ingest-once/main.go:25`. Every production Ingester comes from `ingest.NewWithBuiltinSources` (`internal/ingest/ingest.go:75`). Because the Ingester already holds the `*db.DB` (`ingest.go:35`), it reads the setting itself: new unexported method `rawCutoff`, a `sync.Once` field for the warning, and `Stats.RawCutoff`/`Stats.RawRetention`. No call site changes. The MCP server sees the Ingester only through `leaderIngester` (`internal/cli/mcp.go:120-124`: `IngestAll`, `BackfillActivity`, `PurgeMissing`), which has no sweep method. The sweep is a `cli` function (`sweepRawRetention`) called from `newIndexCmd`'s `RunE` and, through a new `sweep` field on `mcpRuntime` (`mcp.go:98-101`) and `leaderEnv` (`mcp.go:127-137`) that `newMCPRuntime` (`mcp.go:107-116`) fills and the command wires at `mcp.go:76`, once at the end of `startupCatchup` (`mcp.go:334-375`, after `PurgeMissing` at `:366`). The per-call catch-up (`newBeforeRead`, `mcp.go:181`) and `runCatchUp` never call it. MCP's stdout is the protocol channel, so the MCP sweep writes its summary to `io.Discard`.
9. **doctor / show.** doctor's pruned line is `internal/doctor/doctor.go:288-304` inside `RunWith` (`:69`), using `add(name, ok, detail)` (`:71-73`); the CLI opens the DB read-only (`internal/cli/doctor.go:62`). show's note is `prunedNote` (`internal/cli/show.go:188-192`), called at `show.go:100` and `:113`; it writes to `os.Stderr`.
10. **Test helpers reused.** ingest: `writeSession` (`internal/ingest/ingest_test.go:27`), `openTestDB` (`:44`), `appendLine` (`:550`), `rowCount` (`:931`), fixtures `evUser1`/`evAsst1`/`evResult1`/`evUser2` (`:19-23`, dated 2026-04-26, 749 bytes together — past the 512-byte `fingerprintWindow` of `internal/ingest/incremental.go:10`, which an incremental append test needs), `activityRows` (`internal/ingest/activity_test.go:281`). cli: `pruneSandbox` (`internal/cli/prune_test.go:23`, sets HOME/XDG, ingests one 2026-01-01 session, ages it), `rawJSONOf` (`:71`), `mustPrune` (`:132`), `reopen` (`:203`), `runShowCmd` (`:562`), stderr capture as in `TestShowRawFormatPrunedNote` (`:597-613`), `runIndexCmd` (`internal/cli/index_purge_test.go:16`), `testEnv` (`internal/cli/mcp_lease_test.go:97`). doctor: `findResult` (`internal/doctor/doctor_test.go:200`). New helpers are only fixture builders (`recentEvent`, `setRetention`/`putRetention`, `blankRaw`, `runConfigCmd`).
11. **openspec.** Capabilities touched: `raw-json-retention` (ADDED ×4, MODIFIED "pruned raw_json is restorable and restore failure is visible", `openspec/specs/raw-json-retention/spec.md:78-103`), `session-ingest` (ADDED ×1), `diagnostics` (ADDED ×1), `cli-surface` (MODIFIED "show degrades honestly on pruned sessions", `openspec/specs/cli-surface/spec.md:347-369`), `documentation` (MODIFIED "Docs cover every user-facing command and MCP tool", `openspec/specs/documentation/spec.md:30-49`). Validate with `OPENSPEC_TELEMETRY=0 openspec validate raw-json-retention-policy --strict`.

---

## Task 1: openspec change `raw-json-retention-policy`

**Files:**
- Create: `openspec/changes/raw-json-retention-policy/.openspec.yaml` (generated by `openspec new change`)
- Create: `openspec/changes/raw-json-retention-policy/proposal.md`
- Create: `openspec/changes/raw-json-retention-policy/tasks.md`
- Create: `openspec/changes/raw-json-retention-policy/specs/raw-json-retention/spec.md`
- Create: `openspec/changes/raw-json-retention-policy/specs/session-ingest/spec.md`
- Create: `openspec/changes/raw-json-retention-policy/specs/diagnostics/spec.md`
- Create: `openspec/changes/raw-json-retention-policy/specs/cli-surface/spec.md`
- Create: `openspec/changes/raw-json-retention-policy/specs/documentation/spec.md`
- Read only: `openspec/specs/raw-json-retention/spec.md:78-103`, `openspec/specs/cli-surface/spec.md:347-369`, `openspec/specs/documentation/spec.md:30-49` (MODIFIED blocks copy them in full)

**Interfaces:**
- Consumes: design doc §1–§5.
- Produces: the change directory; the requirement names later Tasks implement.

- [ ] **Step 1: Run the validation that must fail**

Run: `OPENSPEC_TELEMETRY=0 openspec validate raw-json-retention-policy --strict`
Expected: `Unknown item 'raw-json-retention-policy'` followed by `Did you mean: raw-json-retention, …`.

- [ ] **Step 2: Create the change skeleton**

Run: `OPENSPEC_TELEMETRY=0 openspec new change raw-json-retention-policy`
Expected: `✔ Created change 'raw-json-retention-policy' at openspec/changes/raw-json-retention-policy/ (schema: spec-driven)`

- [ ] **Step 3: Write `openspec/changes/raw-json-retention-policy/proposal.md`**

````md
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
  `COALESCE(ended_at, started_at, 0)` after that commit is older than `now - window`
  (cutoff computed once per run). Content, FTS, tool calls/targets and usage are
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

- New: `internal/db/migrations/0017_settings.sql`, `internal/db/settings.go`,
  `internal/cli/config.go`.
- Changed: `internal/timeutil/timeutil.go`, `internal/ingest/ingest.go`,
  `internal/cli/{index,prune,show,root,mcp}.go`, `internal/doctor/doctor.go`, both READMEs,
  `docs/USAGE.md`.
````

- [ ] **Step 4: Write `openspec/changes/raw-json-retention-policy/tasks.md`**

````md
## 1. Spec

- [ ] 1.1 Change validates with `openspec validate raw-json-retention-policy --strict`.

## 2. Setting

- [ ] 2.1 Migration 0017 `settings` table; `ParseRetention`; `DB.Setting/PutSetting/DeleteSetting/RawRetention`.
- [ ] 2.2 `clio config raw-retention [VALUE|off]`, registered on the root command.

## 3. Ingest

- [ ] 3.1 Write-time drop for rows each file commit inserts; activity facts from parsed events (pinned by a test with `RepairActivity`).
- [ ] 3.2 Invalid or unreadable setting: raw kept, warning naming the value.

## 4. clio index

- [ ] 4.1 Retention sweep through prune-raw's shared preflight in `clio index` and once at MCP leader startup; per-read catch-ups do not sweep.
- [ ] 4.2 Residual check excludes retention-blanked sessions; help text states the restore path.

## 5. Visibility and docs

- [ ] 5.1 `clio doctor` `raw retention` line; `show --format raw` note names the window.
- [ ] 5.2 READMEs (en, zh-TW) and `docs/USAGE.md`.
````

- [ ] **Step 5: Write `openspec/changes/raw-json-retention-policy/specs/raw-json-retention/spec.md`**

````md
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
store `raw_json = ''` if the session's `COALESCE(ended_at, started_at, 0)` after the
commit is older than the cutoff. Content, FTS, tool calls, tool targets, and usage
SHALL be written as without retention, from the parsed events. Rows written by earlier
commits SHALL NOT be changed by this rule.

#### Scenario: Full reindex with retention
- **WHEN** `clio index --full` runs with retention `14d` over an old session and a
  session active today
- **THEN** every row of the old session has `raw_json = ''` while its content, FTS hits,
  tool calls and tool targets are present, and the recent session keeps raw_json

#### Scenario: Appending to an old session
- **WHEN** an old, already-blanked session file receives a new line stamped now and is
  ingested incrementally
- **THEN** the new row keeps raw_json and the earlier rows stay blank

### Requirement: clio index sweeps sessions that aged past the window
When a window is set, `clio index` (incremental and `--full`) SHALL, after ingest,
blank raw_json of sessions older than the run's cutoff using prune-raw's preflight
(candidate selection, restorability checks, pending-backfill exclusion) and its
in-transaction revalidation. Skipped sessions SHALL be left intact without a warning.
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
retention cutoff is also excepted, because the window keeps it blank on purpose.
The prune command SHALL print the restore path on completion.

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

#### Scenario: Retention off restores
- **WHEN** the user runs `clio config raw-retention off` and then `clio index --full`
- **THEN** raw_json is restored for every session whose source file exists
````

- [ ] **Step 6: Write `openspec/changes/raw-json-retention-policy/specs/session-ingest/spec.md`**

````md
## ADDED Requirements

### Requirement: Every ingest path applies the raw retention window
The write-time raw_json drop of the raw-json-retention capability SHALL apply to every
ingest path — `clio index`, `clio index --full`, the CLI read catch-up, the MCP
leader's startup catch-up and read catch-up, the watcher, and `install-mcp` — and to
every source (Claude Code, Codex). The setting SHALL be read once per `IngestAll` or
`IngestFile` call. The retention sweep SHALL run only in the `clio index` command and
once at the end of the MCP leader's startup catch-up; the catch-ups that run before
reads (MCP per call, CLI commands), the watcher, and `install-mcp` SHALL NOT sweep. Activity facts (`tool_calls`,
`tool_targets`) SHALL come from the parsed events, so a row stored without raw_json
loses none, and the activity repair pass SHALL tolerate such rows.

#### Scenario: Read catch-ups do not sweep
- **WHEN** retention is set and an aged session written with raw_json is unchanged on
  disk, and the MCP per-call catch-up and the CLI read catch-up run
- **THEN** its raw_json is kept, and a later `clio index` blanks it

#### Scenario: Codex rollouts follow the window
- **WHEN** retention is `14d` and a Codex rollout older than 14 days is ingested
- **THEN** its rows store `raw_json = ''`

#### Scenario: Repair pass over blanked rows
- **WHEN** a session was ingested with its raw_json dropped and `RepairActivity` runs
- **THEN** it succeeds and the session's tool targets are unchanged
````

- [ ] **Step 7: Write `openspec/changes/raw-json-retention-policy/specs/diagnostics/spec.md`**

````md
## ADDED Requirements

### Requirement: doctor reports the raw retention setting
`clio doctor` SHALL print a `raw retention` line directly after the `pruned raw_json`
line: the stored window (for example `14d`), or `off` when unset or when the database
has no settings table yet. A stored value that does not parse SHALL be a warning that
names the value and the fix (`clio config raw-retention off` or a valid window).

#### Scenario: Retention set
- **WHEN** retention is `14d` and `clio doctor` runs
- **THEN** the `raw retention` line reads `14d`

#### Scenario: Invalid stored value
- **WHEN** the stored value is `abc` and `clio doctor` runs
- **THEN** the `raw retention` line is a warning naming `abc`
````

- [ ] **Step 8: Write `openspec/changes/raw-json-retention-policy/specs/cli-surface/spec.md`**

````md
## MODIFIED Requirements

### Requirement: show degrades honestly on pruned sessions
`clio show --format raw` and `--format json` SHALL detect messages whose
`raw_json` is the empty pruned sentinel and render an honest signal rather than
empty lines: `--format raw` emits a per-session note that the raw form was
pruned and can be restored with `clio index --full` (and still prints any
non-pruned lines); when a raw retention window is set, the note SHALL name the window
and give the restore path `clio config raw-retention off`, then `clio index --full`;
`--format json` emits `"RawJSON": null` for pruned messages (the pre-existing capitalized message key is preserved for backward compatibility)
and a top-level `"raw_pruned": true` flag. `--format markdown` is unaffected.

#### Scenario: Raw format notes a pruned session
- **WHEN** `clio show <uuid> --format raw` targets a fully pruned session
- **THEN** the output states the raw form was pruned and names the restore
  command, rather than printing blank lines

#### Scenario: Raw format note with retention set
- **WHEN** retention is `14d` and `clio show <uuid> --format raw` targets a pruned
  session
- **THEN** the note names `14d` and `clio config raw-retention off`

#### Scenario: JSON format flags pruned messages
- **WHEN** `clio show <uuid> --format json` targets a session with pruned
  messages
- **THEN** those messages carry `RawJSON: null` (existing key preserved) and the payload carries
  `raw_pruned: true`

#### Scenario: Markdown is unaffected
- **WHEN** `clio show <uuid>` (markdown) targets a pruned session
- **THEN** the output is identical to before pruning (markdown never used
  `raw_json`)
````

- [ ] **Step 9: Write `openspec/changes/raw-json-retention-policy/specs/documentation/spec.md`**

````md
## MODIFIED Requirements

### Requirement: Docs cover every user-facing command and MCP tool

The README (`README.md` and `README_zh-TW.md`) and `docs/USAGE.md` SHALL document
every user-facing CLI command and every MCP tool clio ships. The README MCP tools
table SHALL list every registered tool, and `docs/USAGE.md` SHALL carry a CLI
reference entry (or cheat-sheet line) for every command. The English and
Traditional Chinese READMEs SHALL stay in sync.

#### Scenario: Every MCP tool appears in the README table

- **WHEN** a user reads the MCP tools table in either README
- **THEN** it SHALL list every tool the server registers — `search`, `ask`,
  `list_sessions`, `activity_summary`, and `read_session`

#### Scenario: Every command is documented in the usage guide

- **WHEN** a user reads `docs/USAGE.md`
- **THEN** every user-facing command — including `ask`, `activity`, `recall`,
  `install-hook`, `uninstall-hook`, and `config raw-retention` — SHALL have a
  reference entry or a cheat-sheet line

#### Scenario: Raw retention restore path is documented

- **WHEN** a user reads either README or `docs/USAGE.md`
- **THEN** it SHALL show `clio config raw-retention` and state that restoring raw_json
  under a retention window takes `clio config raw-retention off` and then
  `clio index --full`
````

- [ ] **Step 10: Validate**

Run: `OPENSPEC_TELEMETRY=0 openspec validate raw-json-retention-policy --strict`
Expected: `Change 'raw-json-retention-policy' is valid`
(If it reports `must contain SHALL or MUST`, the first sentence of that requirement lacks SHALL; the text above already passes.)

- [ ] **Step 11: Tick `tasks.md` 1.1, then commit**

```bash
sed -i '' 's/^- \[ \] 1.1 /- [x] 1.1 /' openspec/changes/raw-json-retention-policy/tasks.md
git add openspec/changes/raw-json-retention-policy
git commit -m "spec: raw_json retention policy change"
```

---

## Task 2: `settings` table, `ParseRetention`, and DB accessors (§3.1)

**Files:**
- Modify: `internal/timeutil/timeutil.go:5-10` (imports), `:20-31` (relative branch of `ParseSince`), insert after `:38`
- Create: `internal/timeutil/retention_test.go`
- Create: `internal/db/migrations/0017_settings.sql`
- Create: `internal/db/settings.go`
- Create: `internal/db/settings_test.go`

**Interfaces:**
- Consumes: `timeutil.ParseSince` (`timeutil.go:15`), `db.DB` (embeds `*sql.DB`, `db.go:29-32`), `migrate` (`db.go:108`).
- Produces:
  - `func ParseRetention(s string) (time.Duration, error)` (package `timeutil`); unexported `func parseRelative(s string) (num int, unit time.Duration, ok bool)`
  - `const RawRetentionKey = "raw_retention"` (package `db`)
  - `func (d *DB) Setting(ctx context.Context, key string) (value string, ok bool, err error)`
  - `func (d *DB) PutSetting(ctx context.Context, key, value string) error`
  - `func (d *DB) DeleteSetting(ctx context.Context, key string) error`
  - `func (d *DB) RawRetention(ctx context.Context) (window time.Duration, stored string, err error)` — `(0, "", nil)` when unset; `(0, stored, parseErr)` when the stored value does not parse; `(0, "", queryErr)` when the table cannot be read.

- [ ] **Step 1: Write the failing tests**

`internal/timeutil/retention_test.go`:

````go
package timeutil

import (
	"strings"
	"testing"
	"time"
)

func TestParseRetentionAcceptsRelativeDurations(t *testing.T) {
	cases := map[string]time.Duration{
		"14d":  14 * 24 * time.Hour,
		"12h":  12 * time.Hour,
		"30m":  30 * time.Minute,
		" 7d ": 7 * 24 * time.Hour,
	}
	for in, want := range cases {
		got, err := ParseRetention(in)
		if err != nil || got != want {
			t.Errorf("ParseRetention(%q) = %v, %v; want %v, nil", in, got, err, want)
		}
	}
}

func TestParseRetentionRejectsDatesZeroNegativeGarbage(t *testing.T) {
	for _, bad := range []string{"0d", "-5d", "2026-10-01", "2026-10-01T10:00:00", "abc", "", "1.5d", "14", "d", "999999999999d"} {
		_, err := ParseRetention(bad)
		if err == nil {
			t.Errorf("ParseRetention(%q) accepted, want an error", bad)
			continue
		}
		if !strings.Contains(err.Error(), "positive duration") {
			t.Errorf("ParseRetention(%q) error %q lacks the prune-raw style wording", bad, err)
		}
	}
}
````

`internal/db/settings_test.go`:

````go
package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if _, ok, err := d.Setting(ctx, RawRetentionKey); err != nil || ok {
		t.Fatalf("fresh db: ok=%v err=%v, want unset", ok, err)
	}
	if err := d.PutSetting(ctx, RawRetentionKey, "14d"); err != nil {
		t.Fatal(err)
	}
	if err := d.PutSetting(ctx, RawRetentionKey, "30d"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := d.Setting(ctx, RawRetentionKey); err != nil || !ok || v != "30d" {
		t.Fatalf("got %q ok=%v err=%v, want 30d", v, ok, err)
	}
	if err := d.DeleteSetting(ctx, RawRetentionKey); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.Setting(ctx, RawRetentionKey); ok {
		t.Fatal("key still set after delete")
	}
}

func TestRawRetention(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if w, v, err := d.RawRetention(ctx); w != 0 || v != "" || err != nil {
		t.Fatalf("unset: %v %q %v, want 0 \"\" nil", w, v, err)
	}
	d.PutSetting(ctx, RawRetentionKey, "14d")
	if w, v, err := d.RawRetention(ctx); w != 14*24*time.Hour || v != "14d" || err != nil {
		t.Fatalf("14d: %v %q %v", w, v, err)
	}
	// A value written by hand (or by a future clio) that no longer parses is
	// returned by name with the error, so callers can warn and fail open.
	d.PutSetting(ctx, RawRetentionKey, "abc")
	if w, v, err := d.RawRetention(ctx); w != 0 || v != "abc" || err == nil {
		t.Fatalf("bad value: %v %q %v, want 0 \"abc\" error", w, v, err)
	}
}

func TestMigration0017Settings(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`INSERT INTO settings(key, value) VALUES ('k', NULL)`); err == nil {
		t.Fatal("accepted NULL value")
	}
	if _, err := d.Exec(`INSERT INTO settings(key, value) VALUES ('k', 'v')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO settings(key, value) VALUES ('k', 'w')`); err == nil {
		t.Fatal("accepted a duplicate key")
	}
	if err := d.migrate(); err != nil { // re-running migrations keeps the row
		t.Fatal(err)
	}
	var v string
	if err := d.QueryRow(`SELECT value FROM settings WHERE key='k'`).Scan(&v); err != nil || v != "v" {
		t.Fatalf("after re-migrate: %q %v", v, err)
	}
}
````

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/timeutil/ -run TestParseRetention -count=1`
Expected: build failure — `internal/timeutil/retention_test.go:17:15: undefined: ParseRetention`.

Run: `go test ./internal/db/ -run 'TestSettings|TestRawRetention|TestMigration0017' -count=1`
Expected: build failure — `d.Setting undefined (type *DB has no field or method Setting)` and `undefined: RawRetentionKey`.

- [ ] **Step 3: Implement `ParseRetention` (shared relative parser)**

Apply to `internal/timeutil/timeutil.go`:

````diff
diff --git a/internal/timeutil/timeutil.go b/internal/timeutil/timeutil.go
index 0ab0215..13fa5eb 100644
--- a/internal/timeutil/timeutil.go
+++ b/internal/timeutil/timeutil.go
@@ -4,6 +4,7 @@ package timeutil
 
 import (
 	"fmt"
+	"math"
 	"strconv"
 	"strings"
 	"time"
@@ -17,17 +18,8 @@ func ParseSince(s string) (int64, error) {
 	if s == "" {
 		return 0, nil
 	}
-	if n := len(s); n >= 2 {
-		if num, err := strconv.Atoi(s[:n-1]); err == nil {
-			switch s[n-1] {
-			case 'd':
-				return time.Now().Add(-time.Duration(num) * 24 * time.Hour).Unix(), nil
-			case 'h':
-				return time.Now().Add(-time.Duration(num) * time.Hour).Unix(), nil
-			case 'm':
-				return time.Now().Add(-time.Duration(num) * time.Minute).Unix(), nil
-			}
-		}
+	if num, unit, ok := parseRelative(s); ok {
+		return time.Now().Add(-time.Duration(num) * unit).Unix(), nil
 	}
 	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02"} {
 		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
@@ -37,6 +29,41 @@ func ParseSince(s string) (int64, error) {
 	return 0, fmt.Errorf("invalid time %q (use 7d, 12h, 30m, or YYYY-MM-DD)", s)
 }
 
+// parseRelative splits "7d" / "12h" / "30m" into its count and unit. It is the
+// relative half of ParseSince, shared with ParseRetention so both accept the
+// same spellings.
+func parseRelative(s string) (num int, unit time.Duration, ok bool) {
+	n := len(s)
+	if n < 2 {
+		return 0, 0, false
+	}
+	num, err := strconv.Atoi(s[:n-1])
+	if err != nil {
+		return 0, 0, false
+	}
+	switch s[n-1] {
+	case 'd':
+		return num, 24 * time.Hour, true
+	case 'h':
+		return num, time.Hour, true
+	case 'm':
+		return num, time.Minute, true
+	}
+	return 0, 0, false
+}
+
+// ParseRetention parses a rolling retention window ("14d", "12h", "30m") with
+// ParseSince's relative grammar. An absolute date is rejected because a rolling
+// window has no fixed start; zero, negative and overflowing counts are rejected
+// as well.
+func ParseRetention(s string) (time.Duration, error) {
+	num, unit, ok := parseRelative(strings.TrimSpace(s))
+	if !ok || num <= 0 || int64(num) > math.MaxInt64/int64(unit) {
+		return 0, fmt.Errorf("raw retention must be a positive duration such as 14d, 12h or 30m (not a date), got %q", s)
+	}
+	return time.Duration(num) * unit, nil
+}
+
 // FormatRFC3339 renders a unix timestamp as local RFC 3339, the time format of
 // every MCP tool; 0 (unknown) renders empty.
 func FormatRFC3339(ts int64) string {
````

- [ ] **Step 4: Add the migration**

`internal/db/migrations/0017_settings.sql`:

````sql
-- Settings stored in the index itself (the design rules out a config file and an
-- environment variable). First key: raw_retention, set by `clio config raw-retention`.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
````

- [ ] **Step 5: Add the accessors**

`internal/db/settings.go`:

````go
package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/linhn0617/clio/internal/timeutil"
)

// RawRetentionKey holds the rolling raw_json retention window ("14d"); unset means off.
const RawRetentionKey = "raw_retention"

// Setting returns the stored value for key; ok is false when the key is unset.
func (d *DB) Setting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = d.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// PutSetting stores value under key, replacing any previous value.
func (d *DB) PutSetting(ctx context.Context, key, value string) error {
	_, err := d.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DeleteSetting removes key; deleting an unset key is not an error.
func (d *DB) DeleteSetting(ctx context.Context, key string) error {
	_, err := d.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// RawRetention returns the raw_json retention window, 0 when unset. A stored
// value that no longer parses comes back in stored together with the parse
// error, so callers can name it in a warning while treating retention as off.
func (d *DB) RawRetention(ctx context.Context) (window time.Duration, stored string, err error) {
	v, ok, err := d.Setting(ctx, RawRetentionKey)
	if err != nil || !ok {
		return 0, "", err
	}
	w, err := timeutil.ParseRetention(v)
	if err != nil {
		return 0, v, err
	}
	return w, v, nil
}
````

- [ ] **Step 6: Run the tests and the packages**

Run: `go test ./internal/timeutil/ ./internal/db/ -count=1`
Expected: `ok  github.com/linhn0617/clio/internal/timeutil` and `ok  github.com/linhn0617/clio/internal/db` (the existing `TestParseSince*` tests pin that `ParseSince` is unchanged).

- [ ] **Step 7: Commit**

```bash
git add internal/timeutil/timeutil.go internal/timeutil/retention_test.go internal/db/migrations/0017_settings.sql internal/db/settings.go internal/db/settings_test.go
git commit -m "db: settings table and raw retention parsing"
```

---

## Task 3: `clio config raw-retention [VALUE]` (§3.1)

**Files:**
- Create: `internal/cli/config.go`
- Create: `internal/cli/config_test.go`
- Modify: `internal/cli/root.go:33` (add `newConfigCmd(),` after `newPruneRawCmd(),`)

**Interfaces:**
- Consumes: `config.DBPath`, `db.Open`, `db.(*DB).RawRetention/PutSetting/DeleteSetting`, `db.RawRetentionKey`, `timeutil.ParseRetention` (Task 2); test helpers `pruneSandbox`, `reopen`.
- Produces: `func newConfigCmd() *cobra.Command`, `func newConfigRawRetentionCmd() *cobra.Command`. Behaviour: no arg → prints the stored value or `off` (no DB file → `off`, nothing created); `off` → deletes the key; anything else → `ParseRetention` or a non-zero exit with its error. `DisableFlagParsing` so `-5d` reaches `ParseRetention` (with cobra's default parsing it fails as `unknown shorthand flag: '5' in -5d`, observed in a prototype); `-h`/`--help` are handled explicitly.

- [ ] **Step 1: Write the failing tests**

`internal/cli/config_test.go`:

````go
package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/db"
)

// runConfigCmd runs `clio config <args>` and returns stdout and the error.
func runConfigCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newConfigCmd()
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	return out.String(), err
}

func TestConfigRawRetentionRoundTrip(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-cfg", 1)
	database.Close()

	if out, err := runConfigCmd(t, "raw-retention"); err != nil || strings.TrimSpace(out) != "off" {
		t.Fatalf("unset: out=%q err=%v, want \"off\"", out, err)
	}
	if _, err := runConfigCmd(t, "raw-retention", "14d"); err != nil {
		t.Fatal(err)
	}
	if out, err := runConfigCmd(t, "raw-retention"); err != nil || strings.TrimSpace(out) != "14d" {
		t.Fatalf("after set: out=%q err=%v, want \"14d\"", out, err)
	}
	d := reopen(t)
	v, ok, err := d.Setting(context.Background(), db.RawRetentionKey)
	d.Close()
	if err != nil || !ok || v != "14d" {
		t.Fatalf("stored %q ok=%v err=%v, want 14d", v, ok, err)
	}
	if _, err := runConfigCmd(t, "raw-retention", "off"); err != nil {
		t.Fatal(err)
	}
	if out, _ := runConfigCmd(t, "raw-retention"); strings.TrimSpace(out) != "off" {
		t.Fatalf("after off: out=%q, want \"off\"", out)
	}
}

func TestConfigRawRetentionRejectsBadValues(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-cfg", 1)
	database.Close()
	for _, bad := range []string{"0d", "-5d", "2026-10-01", "abc"} {
		_, err := runConfigCmd(t, "raw-retention", bad)
		if err == nil || !strings.Contains(err.Error(), "positive duration") {
			t.Errorf("raw-retention %q: err=%v, want a positive-duration rejection", bad, err)
		}
	}
	if out, _ := runConfigCmd(t, "raw-retention"); strings.TrimSpace(out) != "off" {
		t.Fatalf("a rejected value was stored: %q", out)
	}
}

func TestRootRegistersConfigRawRetention(t *testing.T) {
	sub, _, err := newRootCmd().Find([]string{"config", "raw-retention"})
	if err != nil || sub.Name() != "raw-retention" {
		t.Fatalf("clio config raw-retention not registered: %v %v", sub, err)
	}
}

func TestConfigRawRetentionHelpStillWorks(t *testing.T) {
	out, err := runConfigCmd(t, "raw-retention", "--help")
	if err != nil || !strings.Contains(out, "clio index --full") {
		t.Fatalf("--help: err=%v out=%q", err, out)
	}
}
````

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/cli/ -run 'TestConfig|TestRootRegistersConfig' -count=1`
Expected: build failure — `internal/cli/config_test.go:15:9: undefined: newConfigCmd`.

- [ ] **Step 3: Implement the command**

`internal/cli/config.go`:

````go
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/timeutil"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or change settings stored in the index",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newConfigRawRetentionCmd())
	return cmd
}

func newConfigRawRetentionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "raw-retention [VALUE]",
		Short: "Show or set how long stored raw_json is kept (e.g. 14d, or off)",
		Long: `With no VALUE, print the raw_json retention window, or "off".

A VALUE such as 14d or 12h keeps raw_json (used only by 'clio show --format
raw|json') only for sessions whose last activity is within that window: every
ingest stores older sessions without it, and 'clio index' blanks sessions that
aged past it, with the same restorability checks as 'clio prune-raw'. The
window rolls, so a date is rejected. "off" removes the setting.

To get the raw form back: 'clio config raw-retention off', then
'clio index --full'. The file shrinks on disk only with
'clio prune-raw --vacuum' (or a VACUUM).`,
		Args: cobra.MaximumNArgs(1),
		// "-5d" must reach ParseRetention and get its wording, not cobra's
		// "unknown shorthand flag"; the command has no flags besides help.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				if _, err := os.Stat(dbPath); err != nil {
					fmt.Fprintln(out, "off") // no index yet: nothing is set
					return nil
				}
			}
			var value string
			if len(args) == 1 {
				value = strings.TrimSpace(args[0])
				if value != "off" {
					if _, err := timeutil.ParseRetention(value); err != nil {
						return err
					}
				}
			}
			database, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer database.Close()
			ctx := cmd.Context()
			switch {
			case len(args) == 0:
				_, stored, err := database.RawRetention(ctx)
				switch {
				case err != nil && stored != "":
					fmt.Fprintf(out, "%s (invalid, treated as off: %v)\n", stored, err)
				case err != nil:
					return err
				case stored == "":
					fmt.Fprintln(out, "off")
				default:
					fmt.Fprintln(out, stored)
				}
				return nil
			case value == "off":
				if err := database.DeleteSetting(ctx, db.RawRetentionKey); err != nil {
					return err
				}
				fmt.Fprintln(out, "raw retention: off — run `clio index --full` to restore raw_json that retention removed")
				return nil
			default:
				if err := database.PutSetting(ctx, db.RawRetentionKey, value); err != nil {
					return err
				}
				fmt.Fprintf(out, "raw retention: %s — raw_json of sessions idle longer than this is not kept; `clio index` removes it from existing ones\n", value)
				return nil
			}
		},
	}
}
````

Register it — apply to `internal/cli/root.go`:

````diff
diff --git a/internal/cli/root.go b/internal/cli/root.go
index 1986ad8..b2ce4ac 100644
--- a/internal/cli/root.go
+++ b/internal/cli/root.go
@@ -31,6 +31,7 @@ func newRootCmd() *cobra.Command {
 		newActivityCmd(),
 		newUsageCmd(),
 		newPruneRawCmd(),
+		newConfigCmd(),
 		newAskCmd(),
 		newTUICmd(),
 		newRecallCmd(),
````

- [ ] **Step 4: Run the tests and the package**

Run: `go test ./internal/cli/ -run 'TestConfig|TestRootRegistersConfig' -count=1`
Expected: `ok  github.com/linhn0617/clio/internal/cli` (4 tests).
Run: `go test ./internal/cli/ -count=1`
Expected: `ok`.

- [ ] **Step 5: Tick `tasks.md` 2.1 and 2.2, then commit**

```bash
sed -i '' 's/^- \[ \] 2.1 /- [x] 2.1 /; s/^- \[ \] 2.2 /- [x] 2.2 /' openspec/changes/raw-json-retention-policy/tasks.md
git add internal/cli/config.go internal/cli/config_test.go internal/cli/root.go openspec/changes/raw-json-retention-policy/tasks.md
git commit -m "cli: clio config raw-retention"
```

---

## Task 4: Drop raw_json at write time on every ingest path (§3.2, §4)

**Files:**
- Modify: `internal/ingest/ingest.go` — imports `:11-21`; `Ingester` struct `:34-48`; `IngestAll` `:119`, `:130`, `:146`; `IngestFile` `:203-206` (+ new `rawCutoff` after it); `ingestOnce` `:210-216`; `ingestFile` signature `:218` and commit call `:340`; `commit` signature `:512`, loop `:566-599`, after `upsertSession` `:645-647`; new `blankExpiredRaw` before `upsertIngestState` (`:660`)
- Modify: `internal/ingest/usage_test.go:184`, `:501` (pass the new `rawCutoff` argument `0`)
- Create: `internal/ingest/raw_retention_test.go`
- Create: `internal/cli/raw_retention_test.go`

**Interfaces:**
- Consumes: `db.(*DB).RawRetention`, `db.(*DB).PutSetting`, `db.RawRetentionKey` (Task 2); helpers `writeSession`, `openTestDB`, `appendLine`, `rowCount`, `activityRows`, `pruneSandbox`, `runIndexCmd`, `reopen`, `rawJSONOf`.
- Produces:
  - `func (ing *Ingester) rawCutoff(ctx context.Context) (cutoff int64, retention string)` — unix seconds, 0 = off; logs once per Ingester through `ing.log.Warn("raw retention setting ignored; keeping raw_json", "value", stored, "err", err)`.
  - `func (ing *Ingester) ingestOnce(ctx context.Context, path string, force bool, rawCutoff int64) (n int, ingested, stale bool, err error)`
  - `func (ing *Ingester) ingestFile(ctx context.Context, path string, force bool, rawCutoff int64) (int, bool, error)`
  - `func (ing *Ingester) commit(kind changeKind, sess model.Session, msgs []model.Message, excludedToolUses []string, usage *usageResult, fs FileState, rawCutoff int64) (int, error)`
  - `func blankExpiredRaw(tx *sql.Tx, uuid string, firstID, cutoff int64) error`
  - Public signatures `IngestAll`, `IngestFile`, `New`, `NewWithBuiltinSources` are unchanged, so `common.go:115`, `mcp.go:181/351`, `watcher.go:81/109`, `install_mcp.go:48`, `cmd/clio-ingest-once/main.go:25` need no edits.

- [ ] **Step 1: Write the failing ingest tests**

`internal/ingest/raw_retention_test.go`:

````go
package ingest

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

// recentEvent is a one-line user event stamped now, so its session is inside
// any retention window the tests set.
func recentEvent(uuid, content string) string {
	return `{"type":"user","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","cwd":"/p","sessionId":"` + uuid + `","message":{"role":"user","content":"` + content + `"}}`
}

func setRetention(t *testing.T, d *db.DB, v string) {
	t.Helper()
	if err := d.PutSetting(context.Background(), db.RawRetentionKey, v); err != nil {
		t.Fatal(err)
	}
}

// blankRaw counts a session's messages with and without raw_json.
func blankRaw(t *testing.T, d *db.DB, uuid string) (blank, kept int) {
	t.Helper()
	blank = rowCount(t, d, `SELECT count(*) FROM messages WHERE session_uuid=? AND raw_json=''`, uuid)
	kept = rowCount(t, d, `SELECT count(*) FROM messages WHERE session_uuid=? AND raw_json<>''`, uuid)
	return blank, kept
}

func TestRawRetentionFullReindexBlanksOldKeepsRecent(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1, evResult1, evUser2) // 2026-04-26
	writeSession(t, projects, "-Users-x-new", "sess-new", recentEvent("sess-new", "fresh work today"))
	database := openTestDB(t)
	setRetention(t, database, "14d")
	if _, err := New(database, nil).IngestAll(context.Background(), projects, true); err != nil {
		t.Fatal(err)
	}
	if blank, kept := blankRaw(t, database, "sess-1"); blank == 0 || kept != 0 {
		t.Fatalf("old session raw_json: %d blank, %d kept; want all blank", blank, kept)
	}
	if blank, kept := blankRaw(t, database, "sess-new"); blank != 0 || kept == 0 {
		t.Fatalf("recent session raw_json: %d blank, %d kept; want all kept", blank, kept)
	}
	// Everything except raw_json is written as today.
	if n := rowCount(t, database, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'validation'`); n == 0 {
		t.Fatal("FTS lost the old session's content")
	}
	if n := rowCount(t, database, `SELECT count(*) FROM tool_calls tc JOIN messages m ON m.id = tc.message_id WHERE m.session_uuid='sess-1'`); n == 0 {
		t.Fatal("old session has no tool_calls")
	}
	if n := rowCount(t, database, `SELECT count(*) FROM tool_targets WHERE session_uuid='sess-1' AND kind='file' AND value='form.go'`); n != 1 {
		t.Fatalf("old session file target count=%d want 1", n)
	}
}

func TestRawRetentionIncrementalNewOldFileBlanks(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1)
	database := openTestDB(t)
	setRetention(t, database, "14d")
	if _, err := New(database, nil).IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	if blank, kept := blankRaw(t, database, "sess-1"); blank == 0 || kept != 0 {
		t.Fatalf("incremental old file: %d blank, %d kept; want all blank", blank, kept)
	}
}

func TestRawRetentionAppendMakesSessionRecent(t *testing.T) {
	projects := t.TempDir()
	// Four lines (749 bytes) put the file past fingerprintWindow (512 bytes,
	// incremental.go), so the append below is ingested incrementally.
	path := writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1, evResult1, evUser2)
	database := openTestDB(t)
	setRetention(t, database, "14d")
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	oldBlank, _ := blankRaw(t, database, "sess-1")
	appendLine(t, path, recentEvent("sess-1", "picked it up again"))
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	blank, kept := blankRaw(t, database, "sess-1")
	if blank != oldBlank || kept != 1 {
		t.Fatalf("after append: %d blank (was %d), %d kept; want earlier rows still blank and the new row kept", blank, oldBlank, kept)
	}
}

// The drop covers only the rows the current commit inserts: rows an earlier
// commit stored with raw_json are left for the sweep (and its restorability
// checks), even when the session is old.
func TestRawRetentionLeavesEarlierCommitsAlone(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1, evResult1, evUser2)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil { // retention off
		t.Fatal(err)
	}
	_, keptBefore := blankRaw(t, database, "sess-1")
	setRetention(t, database, "14d")
	appendLine(t, path, `{"type":"user","timestamp":"2026-04-26T11:10:00Z","sessionId":"sess-1","message":{"role":"user","content":"one more old line"}}`)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	if blank, kept := blankRaw(t, database, "sess-1"); blank != 1 || kept != keptBefore {
		t.Fatalf("after old append: %d blank, %d kept (was %d kept); want only the new row blank", blank, kept, keptBefore)
	}
}

func TestRawRetentionUnsetKeepsRaw(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1)
	database := openTestDB(t)
	if _, err := New(database, nil).IngestAll(context.Background(), projects, true); err != nil {
		t.Fatal(err)
	}
	if blank, _ := blankRaw(t, database, "sess-1"); blank != 0 {
		t.Fatalf("retention unset blanked %d rows", blank)
	}
}

func TestRawRetentionBadStoredValueFailsOpen(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1)
	database := openTestDB(t)
	setRetention(t, database, "2026-10-01") // bypasses the command's validation
	var logBuf bytes.Buffer
	ing := New(database, slog.New(slog.NewTextHandler(&logBuf, nil)))
	if _, err := ing.IngestAll(context.Background(), projects, true); err != nil {
		t.Fatal(err)
	}
	if blank, _ := blankRaw(t, database, "sess-1"); blank != 0 {
		t.Fatalf("invalid retention blanked %d rows; want raw kept (fail open)", blank)
	}
	if !bytes.Contains(logBuf.Bytes(), []byte("value=2026-10-01")) {
		t.Fatalf("no warning naming the bad value:\n%s", logBuf.String())
	}
}

// Design §3.2: activity facts come from the parsed events, so blanking raw_json
// in the same commit loses none, and the repair pass (which reads stored
// raw_json) tolerates the blank rows.
func TestRawRetentionActivityFromParseAndRepairTolerates(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-x-old", "sess-1", evUser1, evAsst1, evResult1)
	database := openTestDB(t)
	setRetention(t, database, "14d")
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, true); err != nil {
		t.Fatal(err)
	}
	if blank, kept := blankRaw(t, database, "sess-1"); blank == 0 || kept != 0 {
		t.Fatalf("fixture not blanked: %d blank, %d kept", blank, kept)
	}
	before := activityRows(t, database, "sess-1")
	if len(before) == 0 {
		t.Fatal("no activity facts written from the parsed events")
	}
	if err := ing.RepairActivity(context.Background()); err != nil {
		t.Fatalf("RepairActivity on blanked rows: %v", err)
	}
	if after := activityRows(t, database, "sess-1"); len(after) != len(before) {
		t.Fatalf("repair changed activity facts: %v -> %v", before, after)
	}
}

// The drop applies to every source: a Codex rollout (2026-06-19) outside the
// window is stored without raw_json.
func TestRawRetentionBlanksOldCodexRollout(t *testing.T) {
	codexRoot := t.TempDir()
	const uuid = "0199dddd-eeee-7fff-8000-222222222222"
	writeCodexRollout(t, codexRoot, uuid)
	database := openTestDB(t)
	setRetention(t, database, "14d")
	ing := New(database, nil)
	ing.AddSource(codexSource{root: codexRoot})
	if _, err := ing.IngestAll(context.Background(), t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	if blank, kept := blankRaw(t, database, uuid); blank == 0 || kept != 0 {
		t.Fatalf("codex rollout raw_json: %d blank, %d kept; want all blank", blank, kept)
	}
}
````

- [ ] **Step 2: Write the failing `clio index` warning test (§4)**

`internal/cli/raw_retention_test.go`:

````go
package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/db"
)

func putRetention(t *testing.T, d *db.DB, v string) {
	t.Helper()
	if err := d.PutSetting(context.Background(), db.RawRetentionKey, v); err != nil {
		t.Fatal(err)
	}
}

// §4: a stored value that no longer parses keeps raw (fail open) and
// `clio index` warns, naming the value.
func TestIndexWarnsOnBadRetentionAndKeepsRaw(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "abc")
	database.Close()
	oldErr := os.Stderr
	rerr, werr, _ := os.Pipe()
	os.Stderr = werr
	_, err := runIndexCmd(t, "--full")
	werr.Close()
	os.Stderr = oldErr
	var eb bytes.Buffer
	eb.ReadFrom(rerr)
	if err != nil {
		t.Fatalf("index --full: %v\n%s", err, eb.String())
	}
	if !strings.Contains(eb.String(), "value=abc") {
		t.Fatalf("no warning naming the bad value:\n%s", eb.String())
	}
	d := reopen(t)
	defer d.Close()
	if raw := rawJSONOf(t, d, "sess-old"); raw[0] == "" {
		t.Fatal("bad retention value blanked raw_json")
	}
}
````

- [ ] **Step 3: Run them and see them fail for the predicted reason**

Run: `go test ./internal/ingest/ -run TestRawRetention -count=1`
Expected (FAIL; `TestRawRetentionUnsetKeepsRaw` passes — it guards today's behaviour):
```
--- FAIL: TestRawRetentionFullReindexBlanksOldKeepsRecent
    raw_retention_test.go:44: old session raw_json: 0 blank, 5 kept; want all blank
--- FAIL: TestRawRetentionIncrementalNewOldFileBlanks
    raw_retention_test.go:70: incremental old file: 0 blank, 3 kept; want all blank
--- FAIL: TestRawRetentionAppendMakesSessionRecent
    raw_retention_test.go:92: after append: 0 blank (was 0), 6 kept; want earlier rows still blank and the new row kept
--- FAIL: TestRawRetentionLeavesEarlierCommitsAlone
    raw_retention_test.go:114: after old append: 0 blank, 6 kept (was 5 kept); want only the new row blank
--- FAIL: TestRawRetentionBadStoredValueFailsOpen
    raw_retention_test.go:144: no warning naming the bad value:
--- FAIL: TestRawRetentionActivityFromParseAndRepairTolerates
    raw_retention_test.go:161: fixture not blanked: 0 blank, 4 kept
--- FAIL: TestRawRetentionBlanksOldCodexRollout
    raw_retention_test.go:189: codex rollout raw_json: 0 blank, 1 kept; want all blank
```

Run: `go test ./internal/cli/ -run TestIndexWarnsOnBadRetention -count=1`
Expected: `raw_retention_test.go:38: no warning naming the bad value:` (FAIL).

- [ ] **Step 4: Implement the write-time drop**

Apply to `internal/ingest/ingest.go` and `internal/ingest/usage_test.go`:

````diff
diff --git a/internal/ingest/ingest.go b/internal/ingest/ingest.go
index 48e9030..ef42988 100644
--- a/internal/ingest/ingest.go
+++ b/internal/ingest/ingest.go
@@ -15,6 +15,7 @@ import (
 	"path/filepath"
 	"sort"
 	"strings"
+	"sync"
 	"time"
 
 	"github.com/linhn0617/clio/internal/db"
@@ -44,6 +45,9 @@ type Ingester struct {
 	// settle paces a forced ingest's retries of a file that changed while it was
 	// read. New sets the production values; tests shorten them.
 	settle settlePolicy
+	// retentionWarn logs an unreadable or invalid raw retention setting once per
+	// Ingester: the MCP server ingests on every read and watcher event.
+	retentionWarn sync.Once
 }
 
 // settlePolicy: before each of up to retries re-ingests, wait until the file's
@@ -116,6 +120,7 @@ func (ing *Ingester) IngestAll(ctx context.Context, projectsDir string, force bo
 		}
 		files = append(files, more...)
 	}
+	cutoff, _ := ing.rawCutoff(ctx)
 	var st Stats
 	seen := make(map[string]bool, len(files))
 	for _, f := range files {
@@ -127,7 +132,7 @@ func (ing *Ingester) IngestAll(ctx context.Context, projectsDir string, force bo
 			return st, err
 		}
 		st.FilesScanned++
-		n, ingested, stale, err := ing.ingestOnce(ctx, f, force)
+		n, ingested, stale, err := ing.ingestOnce(ctx, f, force, cutoff)
 		// A full re-ingest that lost the race with a live writer would otherwise be
 		// lost: the next incremental pass resumes from the old offset. Retry it once
 		// the file settles. An incremental pass just catches up next time.
@@ -143,7 +148,7 @@ func (ing *Ingester) IngestAll(ctx context.Context, projectsDir string, force bo
 			if !settled {
 				break // a re-read of a file still being written would only go stale again
 			}
-			n, ingested, stale, err = ing.ingestOnce(ctx, f, force)
+			n, ingested, stale, err = ing.ingestOnce(ctx, f, force, cutoff)
 		}
 		if err == nil && stale && force {
 			// The last attempt's commit also reads a removed file as changed.
@@ -201,21 +206,40 @@ func (ing *Ingester) waitSettled(ctx context.Context, path string) (bool, error)
 // and whether any ingest happened (false = skipped as unchanged). The whole
 // file is committed in one transaction.
 func (ing *Ingester) IngestFile(ctx context.Context, path string, force bool) (int, bool, error) {
-	n, ingested, _, err := ing.ingestOnce(ctx, path, force)
+	cutoff, _ := ing.rawCutoff(ctx)
+	n, ingested, _, err := ing.ingestOnce(ctx, path, force, cutoff)
 	return n, ingested, err
 }
 
+// rawCutoff turns the raw retention setting into this run's cutoff in unix
+// seconds (0 = keep raw_json) and returns the stored window it came from. It
+// fails open: a setting that cannot be read or no longer parses keeps raw_json,
+// and is logged once.
+func (ing *Ingester) rawCutoff(ctx context.Context) (cutoff int64, retention string) {
+	win, stored, err := ing.db.RawRetention(ctx)
+	if err != nil {
+		ing.retentionWarn.Do(func() {
+			ing.log.Warn("raw retention setting ignored; keeping raw_json", "value", stored, "err", err)
+		})
+		return 0, ""
+	}
+	if win == 0 {
+		return 0, ""
+	}
+	return time.Now().Add(-win).Unix(), stored
+}
+
 // ingestOnce is one IngestFile attempt; stale reports that the commit was
 // aborted because the file changed on disk after it was read.
-func (ing *Ingester) ingestOnce(ctx context.Context, path string, force bool) (n int, ingested, stale bool, err error) {
-	n, ingested, err = ing.ingestFile(ctx, path, force)
+func (ing *Ingester) ingestOnce(ctx context.Context, path string, force bool, rawCutoff int64) (n int, ingested, stale bool, err error) {
+	n, ingested, err = ing.ingestFile(ctx, path, force, rawCutoff)
 	if errors.Is(err, errStaleSnapshot) {
 		return 0, false, true, nil
 	}
 	return n, ingested, false, err
 }
 
-func (ing *Ingester) ingestFile(ctx context.Context, path string, force bool) (int, bool, error) {
+func (ing *Ingester) ingestFile(ctx context.Context, path string, force bool, rawCutoff int64) (int, bool, error) {
 	if err := ctx.Err(); err != nil {
 		return 0, false, err
 	}
@@ -337,7 +361,7 @@ func (ing *Ingester) ingestFile(ctx context.Context, path string, force bool) (i
 			fst.UsagePolicy = usageCountersReplace
 		}
 	}
-	n, err := ing.commit(kind, res.Session, res.Messages, res.ClioIDs, res.Usage, fst)
+	n, err := ing.commit(kind, res.Session, res.Messages, res.ClioIDs, res.Usage, fst, rawCutoff)
 	if err != nil {
 		if errors.Is(err, errSourceConflict) {
 			return 0, false, nil // refused as a cross-source conflict (recorded for doctor)
@@ -509,7 +533,7 @@ func (ing *Ingester) checkSourceConflict(tx *sql.Tx, uuid, source, sourceFile st
 // debt: row-at-a-time inserts inside one transaction per file, no prepared-statement reuse or
 // multi-row batching; revisit if a re-measurement after the redaction prefilter shows commit() dominating.
 
-func (ing *Ingester) commit(kind changeKind, sess model.Session, msgs []model.Message, excludedToolUses []string, usage *usageResult, fs FileState) (int, error) {
+func (ing *Ingester) commit(kind changeKind, sess model.Session, msgs []model.Message, excludedToolUses []string, usage *usageResult, fs FileState, rawCutoff int64) (int, error) {
 	tx, err := ing.db.Begin()
 	if err != nil {
 		return 0, err
@@ -564,6 +588,7 @@ func (ing *Ingester) commit(kind changeKind, sess model.Session, msgs []model.Me
 	}
 
 	inserted := 0
+	var firstID int64 // id of this commit's first inserted row, for the retention pass
 	for _, m := range msgs {
 		res, err := tx.Exec(`INSERT OR IGNORE INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES (?,?,?,?,?,?)`,
 			m.SessionUUID, m.Seq, nullZero(m.TS), m.Role, m.Content, m.RawJSON)
@@ -581,6 +606,11 @@ func (ing *Ingester) commit(kind changeKind, sess model.Session, msgs []model.Me
 			continue
 		}
 		inserted++
+		if firstID == 0 {
+			if firstID, err = res.LastInsertId(); err != nil {
+				return 0, err
+			}
+		}
 		if len(m.ToolCalls) > 0 || len(m.Targets) > 0 {
 			id, err := res.LastInsertId()
 			if err != nil {
@@ -645,6 +675,11 @@ func (ing *Ingester) commit(kind changeKind, sess model.Session, msgs []model.Me
 	if err := ing.upsertSession(tx, sess, totalUserTurns, kind); err != nil {
 		return 0, err
 	}
+	if rawCutoff > 0 && firstID > 0 {
+		if err := blankExpiredRaw(tx, sess.UUID, firstID, rawCutoff); err != nil {
+			return 0, err
+		}
+	}
 
 	monotonic := kind != changeFull
 	if err := ing.upsertIngestState(tx, fs, monotonic); err != nil {
@@ -657,6 +692,25 @@ func (ing *Ingester) commit(kind changeKind, sess model.Session, msgs []model.Me
 	return inserted, nil
 }
 
+// blankExpiredRaw applies the raw retention window to the rows this commit
+// inserted: ids from firstID up (AUTOINCREMENT ids only grow, and this
+// transaction holds the write lock). The age is read back after upsertSession,
+// so it is exactly the COALESCE(ended_at, started_at, 0) that prune-raw
+// compares. A raw_json-only UPDATE leaves FTS alone (migration 0012).
+func blankExpiredRaw(tx *sql.Tx, uuid string, firstID, cutoff int64) error {
+	var last int64
+	if err := tx.QueryRow(`SELECT COALESCE(ended_at, started_at, 0) FROM sessions WHERE uuid = ?`, uuid).Scan(&last); err != nil {
+		return fmt.Errorf("raw retention: read session age: %w", err)
+	}
+	if last >= cutoff {
+		return nil
+	}
+	if _, err := tx.Exec(`UPDATE messages SET raw_json = '' WHERE id >= ? AND session_uuid = ?`, firstID, uuid); err != nil {
+		return fmt.Errorf("raw retention: blank raw_json: %w", err)
+	}
+	return nil
+}
+
 // upsertIngestState writes the file watermark into ingest_state. When monotonic
 // is true (incremental ingests), the update only applies when the new offset is
 // at least as large as the stored one — preventing a stale writer from moving
diff --git a/internal/ingest/usage_test.go b/internal/ingest/usage_test.go
index bb88045..74d6e5f 100644
--- a/internal/ingest/usage_test.go
+++ b/internal/ingest/usage_test.go
@@ -181,7 +181,7 @@ func TestUsageScanFailedIsAtomicNoopFlaggedStale(t *testing.T) {
 		SourceFile: path, LastSize: fi.Size(), LastMTime: fi.ModTime().UnixNano(),
 		LastByteOffset: res.Consumed, UsageStale: 1, UsagePolicy: usageCountersPreserve,
 	}
-	if _, err := ing.commit(changeFull, res.Session, res.Messages, res.ClioIDs, failed, fst); err != nil {
+	if _, err := ing.commit(changeFull, res.Session, res.Messages, res.ClioIDs, failed, fst, 0); err != nil {
 		t.Fatal(err)
 	}
 	after := readUsageRows(t, database, "sess-u")
@@ -498,7 +498,7 @@ func benchTailCommitLong(b *testing.B, withUsage bool) {
 		fst := FileState{SourceFile: path, LastSize: fi.Size(), LastMTime: fi.ModTime().UnixNano(),
 			LastByteOffset: offset + res.Consumed, UsagePolicy: usageCountersReplace}
 		b.StartTimer()
-		if _, err := ing.commit(changeIncremental, res.Session, res.Messages, res.ClioIDs, usage, fst); err != nil {
+		if _, err := ing.commit(changeIncremental, res.Session, res.Messages, res.ClioIDs, usage, fst, 0); err != nil {
 			b.Fatal(err)
 		}
 		b.StopTimer()
````

- [ ] **Step 5: Run the tests and the packages**

Run: `go test ./internal/ingest/ -run TestRawRetention -count=1`
Expected: `ok` (8 tests).
Run: `go test ./internal/cli/ -run TestIndexWarnsOnBadRetention -count=1`
Expected: `ok`.
Run: `go test ./internal/ingest/ -count=1` (about 25 s) and `go test ./internal/cli/ -count=1`
Expected: both `ok`.

Negative control (do not commit it): in `blankExpiredRaw` change the UPDATE's arguments from `firstID, uuid)` to `0, uuid)`; confirm it landed with `grep -c ', 0, uuid)' internal/ingest/ingest.go` (prints `1`); run `go test ./internal/ingest/ -run TestRawRetention -count=1` and expect `TestRawRetentionLeavesEarlierCommitsAlone` to FAIL with `after old append: 6 blank, 0 kept (was 5 kept); want only the new row blank`. Change it back to `firstID, uuid)` by hand and confirm the same grep prints `0`.

- [ ] **Step 6: Tick `tasks.md` 3.1 and 3.2, then commit**

```bash
sed -i '' 's/^- \[ \] 3.1 /- [x] 3.1 /; s/^- \[ \] 3.2 /- [x] 3.2 /' openspec/changes/raw-json-retention-policy/tasks.md
git add internal/ingest/ingest.go internal/ingest/usage_test.go internal/ingest/raw_retention_test.go internal/cli/raw_retention_test.go openspec/changes/raw-json-retention-policy/tasks.md
git commit -m "ingest: drop raw_json of sessions outside the retention window at write time"
```

---

## Task 5: Retention sweep in `clio index` and once at MCP leader startup (§3.3, §4, §5)

**Files:**
- Modify: `internal/ingest/ingest.go` — `Stats` `:88-97`; `IngestAll` line `cutoff, _ := ing.rawCutoff(ctx)` / `var st Stats` (from Task 4)
- Modify: `internal/cli/prune.go:113-130` (preflight → `pruneEligible`), `:138` (`len(candidates)` → `candidates`), insert `pruneEligible` before `classifyPrunable` (`:164`)
- Modify: `internal/cli/index.go` — imports `:14-19`; after the purge block `:86-87`; residual call `:97-101`; new `sweepRawRetention` before `printIndexSummary` (`:156`)
- Modify: `internal/cli/mcp.go` — imports `:4-10`; `mcpRuntime` `:98-101`; `newMCPRuntime` `:107-116`; `leaderEnv` `:127-137`; wiring `:76`; `startupCatchup` IngestAll `:351-353` and after PurgeMissing `:366-371`
- Modify: `internal/ingest/raw_retention_test.go` (append), `internal/cli/raw_retention_test.go` (imports + append, twice)

**Interfaces:**
- Consumes: `rawCutoff` (Task 4); `newBeforeRead` (`mcp.go:155`), `newMCPRuntime` (`mcp.go:107`); `sessions.PruneCandidates`, `sessions.PendingBackfillSessions`, `sessions.PruneRawJSON`, `classifyPrunable`, `humanBytes`, `registry.All`, `ing.ExtraRoots()` (`internal/ingest/source.go:128`); helpers `startupCatchup` (`mcp.go:334`), `runCatchUp` (`common.go:108`), `testEnv`, `discardLogger`.
- Produces:
  - `Stats.RawCutoff int64`, `Stats.RawRetention string`
  - `func pruneEligible(ctx context.Context, database *db.DB, roots []string, cutoff int64, source, project string) (candidates int, eligible []sessions.PrunePair, skips map[string]int, err error)`
  - `func sweepRawRetention(ctx context.Context, database *db.DB, roots []string, cutoff int64, retention string, stdout io.Writer) error` — prints `raw retention <window>: pruned N messages across M sessions (<size> of raw_json)` only when N > 0.
  - `clio index` returns `raw retention sweep failed: <err>` (non-zero) after its other post-ingest steps when the sweep fails.
  - `mcpRuntime.sweep` and `leaderEnv.sweep`, both `func(ctx context.Context, st ingest.Stats) error`; `startupCatchup` calls `env.sweep(wctx, st)` once after `PurgeMissing` when the startup `IngestAll` succeeded, logs `raw retention sweep failed` on error, and still publishes the leader flag.

- [ ] **Step 1: Write the failing `Stats` test**

Append to `internal/ingest/raw_retention_test.go`:

````go
func TestIngestAllReportsRawCutoff(t *testing.T) {
	projects := t.TempDir()
	writeSession(t, projects, "-Users-x-old", "sess-1", evUser1)
	database := openTestDB(t)
	st, err := New(database, nil).IngestAll(context.Background(), projects, false)
	if err != nil || st.RawCutoff != 0 || st.RawRetention != "" {
		t.Fatalf("unset: cutoff=%d retention=%q err=%v, want 0 \"\"", st.RawCutoff, st.RawRetention, err)
	}
	setRetention(t, database, "14d")
	want := time.Now().Add(-14 * 24 * time.Hour).Unix()
	st, err = New(database, nil).IngestAll(context.Background(), projects, false)
	if err != nil || st.RawRetention != "14d" || st.RawCutoff < want-5 || st.RawCutoff > want+5 {
		t.Fatalf("14d: cutoff=%d (want ~%d) retention=%q err=%v", st.RawCutoff, want, st.RawRetention, err)
	}
}
````

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/ingest/ -run TestIngestAllReportsRawCutoff -count=1`
Expected: build failure — `st.RawCutoff undefined (type Stats has no field or method RawCutoff)`.

- [ ] **Step 3: Report the cutoff in `Stats`**

Apply to `internal/ingest/ingest.go`:

````diff
diff --git a/internal/ingest/ingest.go b/internal/ingest/ingest.go
index ef42988..bf0f07c 100644
--- a/internal/ingest/ingest.go
+++ b/internal/ingest/ingest.go
@@ -97,6 +97,11 @@ type Stats struct {
 	// StillChanging lists files a forced ingest gave up on because they kept
 	// changing while being read; they are also counted in FilesSkipped.
 	StillChanging []string
+	// RawCutoff is the unix-seconds cutoff this run dropped raw_json against
+	// (0 = retention off), and RawRetention the stored window it came from.
+	// `clio index` reuses both for its sweep and its residual check.
+	RawCutoff    int64
+	RawRetention string
 }
 
 // IngestAll walks projectsDir and ingests every .jsonl file. force re-ingests
@@ -120,8 +125,8 @@ func (ing *Ingester) IngestAll(ctx context.Context, projectsDir string, force bo
 		}
 		files = append(files, more...)
 	}
-	cutoff, _ := ing.rawCutoff(ctx)
-	var st Stats
+	cutoff, retention := ing.rawCutoff(ctx)
+	st := Stats{RawCutoff: cutoff, RawRetention: retention}
 	seen := make(map[string]bool, len(files))
 	for _, f := range files {
 		if seen[f] { // de-dup overlapping roots
````

Run: `go test ./internal/ingest/ -run 'TestIngestAllReportsRawCutoff|TestRawRetention' -count=1`
Expected: `ok`.

- [ ] **Step 4: Write the failing command-level sweep tests**

Replace the import block of `internal/cli/raw_retention_test.go` with:

```go
import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)
```

Append to `internal/cli/raw_retention_test.go`:

````go
// A session written while retention was off (raw kept) and older than the window
// is pruned by the sweep at the end of `clio index`.
func TestIndexSweepPrunesAgedSession(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	database.Close()
	out, err := runIndexCmd(t)
	if err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	if !strings.Contains(out, "raw retention 14d: pruned 1 messages across 1 sessions") {
		t.Fatalf("missing sweep summary:\n%s", out)
	}
	d := reopen(t)
	defer d.Close()
	if raw := rawJSONOf(t, d, "sess-old"); raw[0] != "" {
		t.Fatalf("aged session not swept: %q", raw[0])
	}
}

// A session whose transcript went missing is skipped by the sweep (prune-raw's
// "missing" reason), and a skip prints nothing.
func TestIndexSweepSkipsMissingSource(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	database.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	out, err := runIndexCmd(t)
	if err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	if strings.Contains(out, "raw retention") {
		t.Fatalf("sweep reported a skipped session:\n%s", out)
	}
	d := reopen(t)
	defer d.Close()
	if raw := rawJSONOf(t, d, "sess-old"); raw[0] == "" {
		t.Fatal("missing-source session was swept")
	}
}

// The catch-ups that run before reads — the MCP per-call one (beforeRead,
// mcp.go:181) and the CLI one (runCatchUp, common.go:108) — do not sweep: an aged
// session stays intact through them, and `clio index` prunes it.
func TestReadCatchUpsDoNotSweep(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	projects := filepath.Dir(filepath.Dir(path))
	var ing leaderIngester = ingest.New(database, nil)
	var notLeader atomic.Bool
	newBeforeRead(context.Background(), testEnv(ing, time.Hour), &notLeader, projects, discardLogger())(context.Background())
	runCatchUp(context.Background(), ing, projects, discardLogger())
	if raw := rawJSONOf(t, database, "sess-old"); raw[0] == "" {
		t.Fatal("a read catch-up swept an aged session")
	}
	database.Close()
	if _, err := runIndexCmd(t); err != nil {
		t.Fatal(err)
	}
	d := reopen(t)
	defer d.Close()
	if raw := rawJSONOf(t, d, "sess-old"); raw[0] != "" {
		t.Fatal("clio index did not sweep the session the catch-ups left")
	}
}

// §4: a sweep failure does not undo the ingest that already committed, and
// `clio index` exits non-zero. The sweep's pending-backfill read is made to fail
// by renaming the watermark table it queries; ingest does not need that table.
func TestIndexSweepFailureExitsNonZeroAfterIngest(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	if _, err := database.Exec(`ALTER TABLE activity_backfill_state RENAME TO activity_backfill_state_gone`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	line := `{"type":"user","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","cwd":"/p","sessionId":"sess-new","message":{"role":"user","content":"new work"}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "sess-new.jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runIndexCmd(t)
	if err == nil || !strings.Contains(err.Error(), "raw retention sweep failed") {
		t.Fatalf("err = %v, want the sweep failure", err)
	}
	d := reopen(t)
	defer d.Close()
	if len(rawJSONOf(t, d, "sess-new")) == 0 {
		t.Fatal("the ingest before the failed sweep was not committed")
	}
}
````

- [ ] **Step 5: Run them and see them fail**

Run: `go test ./internal/cli/ -run 'TestIndexSweep|TestReadCatchUpsDoNotSweep' -count=1 -v`
Expected:
- `TestIndexSweepPrunesAgedSession` FAIL at `raw_retention_test.go:62`: `missing sweep summary:` followed by the index output.
- `TestReadCatchUpsDoNotSweep` FAIL at `:115`: `clio index did not sweep the session the catch-ups left` (its first assertion — the MCP per-call catch-up and the CLI catch-up keep raw — already holds; the failing line proves the fixture is sweepable, so the first assertion is not vacuous). This replaces the earlier draft's `TestMCPCatchUpDoesNotSweep`, which drove `startupCatchup`; that path now sweeps (Steps 10–13).
- `TestIndexSweepFailureExitsNonZeroAfterIngest` FAIL at `:135`: `err = <nil>, want the sweep failure`.
- `TestIndexSweepSkipsMissingSource` PASS (guard: there is no sweep yet). Its red is shown in Step 9.

- [ ] **Step 6: Write the failing direct sweep test**

Append to `internal/cli/raw_retention_test.go`:

````go
// A session whose file no longer matches the stored snapshot (prune-raw's
// "changed" reason) is skipped; with the snapshot matching again it is pruned.
// `clio index` would re-ingest a changed file before the sweep, so the sweep is
// driven directly here.
func TestSweepRawRetentionSkipsChangedSource(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	roots := []string{filepath.Dir(filepath.Dir(path))}
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	if _, err := database.Exec(`UPDATE ingest_state SET last_mtime = last_mtime - 1 WHERE source_file = ?`, path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := sweepRawRetention(context.Background(), database, roots, cutoff, "14d", &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || rawJSONOf(t, database, "sess-old")[0] == "" {
		t.Fatalf("changed session swept (out=%q)", out.String())
	}
	if _, err := database.Exec(`UPDATE ingest_state SET last_mtime = last_mtime + 1 WHERE source_file = ?`, path); err != nil {
		t.Fatal(err)
	}
	if err := sweepRawRetention(context.Background(), database, roots, cutoff, "14d", &out); err != nil {
		t.Fatal(err)
	}
	if rawJSONOf(t, database, "sess-old")[0] != "" {
		t.Fatalf("matching session not swept (out=%q)", out.String())
	}
}
````

Run: `go test ./internal/cli/ -run TestSweepRawRetentionSkipsChangedSource -count=1`
Expected: build failure — `internal/cli/raw_retention_test.go:156:12: undefined: sweepRawRetention`.

- [ ] **Step 7: Extract prune-raw's preflight and add the sweep**

Apply to `internal/cli/prune.go` and `internal/cli/index.go` (the `--full` help text and residual changes come in Task 6; this diff is only the extraction and the sweep):

````diff
diff --git a/internal/cli/index.go b/internal/cli/index.go
index adda1f4..641bf59 100644
--- a/internal/cli/index.go
+++ b/internal/cli/index.go
@@ -15,6 +15,7 @@ import (
 	"github.com/linhn0617/clio/internal/db"
 	"github.com/linhn0617/clio/internal/ingest"
 	"github.com/linhn0617/clio/internal/lock"
+	"github.com/linhn0617/clio/internal/registry"
 	"github.com/linhn0617/clio/internal/sessions"
 	"github.com/linhn0617/clio/internal/termsafe"
 )
@@ -85,6 +86,10 @@ func newIndexCmd() *cobra.Command {
 				}
 				fmt.Fprintf(os.Stdout, "purged: %d session source(s) whose transcript no longer exists\n", n)
 			}
+			// A sweep failure does not undo the committed ingest; it fails the
+			// command after the remaining checks have run.
+			roots := append([]string{projects}, ing.ExtraRoots()...)
+			sweepErr := sweepRawRetention(cmd.Context(), database, roots, st.RawCutoff, st.RawRetention, os.Stdout)
 			if full {
 				// After --purge-missing so its deletes' tombstones are merged too.
 				fmt.Fprintln(os.Stdout, "optimizing fts index…")
@@ -96,9 +101,12 @@ func newIndexCmd() *cobra.Command {
 			}
 			if full {
 				if err := checkPrunedResiduals(cmd.Context(), database, os.Stderr, st.StillChanging); err != nil {
-					return err
+					return errors.Join(err, sweepErr)
 				}
 			}
+			if sweepErr != nil {
+				return fmt.Errorf("raw retention sweep failed: %w", sweepErr)
+			}
 			return nil
 		},
 	}
@@ -153,6 +161,30 @@ func checkPrunedResiduals(ctx context.Context, database *db.DB, stderr io.Writer
 	return nil
 }
 
+// sweepRawRetention prunes raw_json of sessions that aged past the retention
+// window since they were written, with prune-raw's preflight (pruneEligible)
+// and in-transaction revalidation (PruneRawJSON). cutoff 0 means retention is
+// off. Skipped sessions are left intact without a message; one summary line is
+// printed only when something was pruned.
+func sweepRawRetention(ctx context.Context, database *db.DB, roots []string, cutoff int64, retention string, stdout io.Writer) error {
+	if cutoff == 0 {
+		return nil
+	}
+	_, eligible, _, err := pruneEligible(ctx, database, roots, cutoff, registry.All, "")
+	if err != nil {
+		return err
+	}
+	pr, err := sessions.PruneRawJSON(ctx, database, cutoff, registry.All, "", eligible, false)
+	if err != nil {
+		return err
+	}
+	if pr.Messages > 0 {
+		fmt.Fprintf(stdout, "raw retention %s: pruned %d messages across %d sessions (%s of raw_json)\n",
+			retention, pr.Messages, pr.PrunedSessions, humanBytes(pr.RemovedBytes))
+	}
+	return nil
+}
+
 // printIndexSummary reports an index pass: the counts on stdout, and on stderr
 // the files a full reindex skipped because they kept changing while being read
 // (a live session), which keep their older rows.
diff --git a/internal/cli/prune.go b/internal/cli/prune.go
index cda1186..f1d714e 100644
--- a/internal/cli/prune.go
+++ b/internal/cli/prune.go
@@ -110,24 +110,10 @@ func runPruneRaw(ctx context.Context, o pruneOpts) error {
 	}
 	roots = append(roots, ingest.NewWithBuiltinSources(database, discardLogger()).ExtraRoots()...)
 
-	candidates, err := sessions.PruneCandidates(ctx, database, o.cutoff, o.source, o.project)
+	candidates, eligible, skips, err := pruneEligible(ctx, database, roots, o.cutoff, o.source, o.project)
 	if err != nil {
 		return err
 	}
-	pending, err := sessions.PendingBackfillSessions(ctx, database)
-	if err != nil {
-		return err
-	}
-
-	var eligible []sessions.PrunePair
-	skips := map[string]int{} // reason -> count
-	for _, c := range candidates {
-		if reason := classifyPrunable(c, roots, pending); reason != "" {
-			skips[reason]++
-			continue
-		}
-		eligible = append(eligible, sessions.PrunePair{UUID: c.UUID, SourceFile: c.SourceFile})
-	}
 
 	// PruneRawJSON re-validates eligibility inside the write transaction and
 	// returns AUTHORITATIVE counts (validated vs newly-pruned vs raced-out).
@@ -135,7 +121,7 @@ func runPruneRaw(ctx context.Context, o pruneOpts) error {
 	if err != nil {
 		return err
 	}
-	printPruneSummary(o, len(candidates), pr.PrunedSessions, pr.Messages, pr.RemovedBytes, skips)
+	printPruneSummary(o, candidates, pr.PrunedSessions, pr.Messages, pr.RemovedBytes, skips)
 	// RacedOut = preflight-eligible sessions the in-transaction revalidation
 	// rejected (a concurrent change moved/aged/rescoped them) — distinct from an
 	// idempotent rerun (validated but already empty). Reported in real AND dry runs.
@@ -161,6 +147,29 @@ func runPruneRaw(ctx context.Context, o pruneOpts) error {
 	return nil
 }
 
+// pruneEligible is prune-raw's preflight, shared with the retention sweep in
+// `clio index`: sessions older than cutoff, minus those classifyPrunable rules
+// out, with the skip count per reason. PruneRawJSON re-validates the result.
+func pruneEligible(ctx context.Context, database *db.DB, roots []string, cutoff int64, source, project string) (candidates int, eligible []sessions.PrunePair, skips map[string]int, err error) {
+	cands, err := sessions.PruneCandidates(ctx, database, cutoff, source, project)
+	if err != nil {
+		return 0, nil, nil, err
+	}
+	pending, err := sessions.PendingBackfillSessions(ctx, database)
+	if err != nil {
+		return 0, nil, nil, err
+	}
+	skips = map[string]int{} // reason -> count
+	for _, c := range cands {
+		if reason := classifyPrunable(c, roots, pending); reason != "" {
+			skips[reason]++
+			continue
+		}
+		eligible = append(eligible, sessions.PrunePair{UUID: c.UUID, SourceFile: c.SourceFile})
+	}
+	return len(cands), eligible, skips, nil
+}
+
 // classifyPrunable returns "" when the candidate is prunable, else a skip reason.
 // The DB-side facts (aborted, offset) and filesystem facts (regular/readable/
 // under-root/size+mtime match) together establish restorability; pending
````

- [ ] **Step 8: Run the tests and the packages**

Run: `go test ./internal/cli/ -run 'TestIndexSweep|TestSweepRawRetention|TestReadCatchUpsDoNotSweep|TestIndexWarnsOnBadRetention' -count=1`
Expected: `ok`.
Run: `go test ./internal/cli/ -run TestPrune -count=1 -v | grep -c '^--- PASS'`
Expected: `24` (prune-raw unchanged by the extraction).
Run: `go test ./internal/cli/ ./internal/ingest/ -count=1`
Expected: both `ok`.

- [ ] **Step 9: Negative control for the skip guards (do not commit it)**

In `pruneEligible`, change `reason != "" {` to `reason != "" && false {`; confirm it landed with `grep -c 'reason != "" && false' internal/cli/prune.go` (prints `1`); run `go test ./internal/cli/ -run 'TestIndexSweepSkipsMissingSource|TestSweepRawRetentionSkipsChangedSource' -count=1` and expect both to FAIL (`sweep reported a skipped session:` and `changed session swept (out="raw retention 14d: pruned 1 messages across 1 sessions …")`); revert the edit by hand and confirm the same grep prints `0`.

- [ ] **Step 10: Write the failing MCP startup tests**

Replace the import block of `internal/cli/raw_retention_test.go` with:

```go
import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)
```

Append to `internal/cli/raw_retention_test.go`:

````go
// The MCP leader's startup catch-up (mcp.go:351) sweeps once, through the
// runtime newMCPRuntime builds for `clio mcp`.
func TestMCPStartupCatchUpSweeps(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	projects := filepath.Dir(filepath.Dir(path))
	rt := newMCPRuntime(database, projects, discardLogger(), nil)
	env := testEnv(rt.ing, time.Hour)
	env.sweep = rt.sweep
	var flag atomic.Bool
	if !startupCatchup(context.Background(), &flag, env, projects, discardLogger(), false) {
		t.Fatal("startup catch-up cancelled")
	}
	if raw := rawJSONOf(t, database, "sess-old"); raw[0] != "" {
		t.Fatal("MCP startup catch-up did not sweep the aged session")
	}
}

// A failed startup sweep is logged and the leader still starts.
func TestMCPStartupSweepFailureOnlyLogs(t *testing.T) {
	database, path := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	projects := filepath.Dir(filepath.Dir(path))
	env := testEnv(ingest.New(database, nil), time.Hour)
	env.sweep = func(context.Context, ingest.Stats) error { return errors.New("boom") }
	var logBuf bytes.Buffer
	var flag atomic.Bool
	if !startupCatchup(context.Background(), &flag, env, projects, slog.New(slog.NewTextHandler(&logBuf, nil)), false) || !flag.Load() {
		t.Fatal("a sweep failure stopped the leader's startup")
	}
	if !strings.Contains(logBuf.String(), "raw retention sweep failed") || !strings.Contains(logBuf.String(), "boom") {
		t.Fatalf("sweep failure not logged:\n%s", logBuf.String())
	}
}
````

- [ ] **Step 11: Run them and see them fail**

Run: `go test ./internal/cli/ -run TestMCPStartup -count=1`
Expected: build failure — `internal/cli/raw_retention_test.go:183:6: env.sweep undefined (type *leaderEnv has no field or method sweep)` and `rt.sweep undefined (type mcpRuntime has no field or method sweep)`.

- [ ] **Step 12: Run the sweep once at MCP leader startup**

Apply to `internal/cli/mcp.go`:

````diff
diff --git a/internal/cli/mcp.go b/internal/cli/mcp.go
index 674026b..fa8b808 100644
--- a/internal/cli/mcp.go
+++ b/internal/cli/mcp.go
@@ -4,6 +4,7 @@ import (
 	"context"
 	"errors"
 	"fmt"
+	"io"
 	"log/slog"
 	"sync"
 	"sync/atomic"
@@ -73,7 +74,7 @@ func newMCPCmd() *cobra.Command {
 			// already being renewed).
 			env := &leaderEnv{ingestMu: &sync.Mutex{}, renewEvery: leaderRenewInterval}
 			rt := newMCPRuntime(rw, projects, log, env.tryWalk)
-			env.ing, env.startWatcher = rt.ing, rt.watch
+			env.ing, env.startWatcher, env.sweep = rt.ing, rt.watch, rt.sweep
 			beforeRead := newBeforeRead(ctx, env, &isLeaderNow, projects, log)
 
 			waitRole := startLeaseRole(ctx, lease, isLeader, &isLeaderNow, env, projects, log)
@@ -98,6 +99,9 @@ var serveMCP = mcp.Serve
 type mcpRuntime struct {
 	ing   leaderIngester
 	watch func(ctx context.Context)
+	// sweep is the raw retention sweep the leader runs once after its startup
+	// catch-up; nil skips it.
+	sweep func(ctx context.Context, st ingest.Stats) error
 }
 
 // runWatcher is a seam so tests can capture the watcher newMCPRuntime builds.
@@ -112,6 +116,10 @@ var newMCPRuntime = func(rw *db.DB, projects string, log *slog.Logger, guard fun
 		if err := runWatcher(ctx, w); err != nil {
 			log.Warn("watcher stopped", "err", err)
 		}
+	}, sweep: func(ctx context.Context, st ingest.Stats) error {
+		// stdout carries the MCP protocol, so the summary line is dropped.
+		roots := append([]string{projects}, ing.ExtraRoots()...)
+		return sweepRawRetention(ctx, rw, roots, st.RawCutoff, st.RawRetention, io.Discard)
 	}}
 }
 
@@ -134,6 +142,8 @@ type leaderEnv struct {
 	renewEvery time.Duration
 	// startWatcher blocks until ctx is done.
 	startWatcher func(ctx context.Context)
+	// sweep runs once at the end of the startup catch-up (never before reads).
+	sweep func(ctx context.Context, st ingest.Stats) error
 }
 
 // tryWalk takes ingestMu without blocking. It returns the release func, or nil if
@@ -348,8 +358,9 @@ func startupCatchup(wctx context.Context, leaderFlag *atomic.Bool, env *leaderEn
 		}
 		return true
 	}
-	if _, err := env.ing.IngestAll(wctx, projects, false); err != nil && wctx.Err() == nil {
-		log.Warn("startup catch-up failed", "err", err)
+	st, ingestErr := env.ing.IngestAll(wctx, projects, false)
+	if ingestErr != nil && wctx.Err() == nil {
+		log.Warn("startup catch-up failed", "err", ingestErr)
 	}
 	if cancelled() {
 		return false
@@ -369,6 +380,16 @@ func startupCatchup(wctx context.Context, leaderFlag *atomic.Bool, env *leaderEn
 	if cancelled() {
 		return false
 	}
+	// Raw retention sweep, once per leader start: a failure is logged and the
+	// leader still starts (write-time drop keeps working either way).
+	if env.sweep != nil && ingestErr == nil {
+		if err := env.sweep(wctx, st); err != nil && wctx.Err() == nil {
+			log.Warn("raw retention sweep failed", "err", err)
+		}
+		if cancelled() {
+			return false
+		}
+	}
 	// Catch-up done: safe to serve reads without follower catch-up.
 	leaderFlag.Store(true)
 	return true
````

Test stubs that build `mcpRuntime{ing: …, watch: …}` (`internal/cli/mcp_lease_test.go:437`, `internal/cli/mcp_round2_test.go:114`) and `testEnv` leave `sweep` nil, which skips the sweep; they need no change.

- [ ] **Step 13: Run the tests, the package, and a negative control**

Run: `go test ./internal/cli/ -run 'TestMCPStartup|TestReadCatchUpsDoNotSweep' -count=1`
Expected: `ok`.
Run: `go test ./internal/cli/ -count=1`
Expected: `ok` (includes the existing lease/startup tests in `mcp_lease_test.go` and `mcp_round2_test.go`).

Negative control (do not commit it): in `startupCatchup` change `if env.sweep != nil && ingestErr == nil {` to `if env.sweep != nil && ingestErr == nil && false {`; confirm with `grep -c 'ingestErr == nil && false' internal/cli/mcp.go` (prints `1`); run `go test ./internal/cli/ -run TestMCPStartup -count=1` and expect `MCP startup catch-up did not sweep the aged session` and `sweep failure not logged:`; revert by hand and confirm the grep prints `0`.

- [ ] **Step 14: Tick `tasks.md` 4.1, then commit**

```bash
sed -i '' 's/^- \[ \] 4.1 /- [x] 4.1 /' openspec/changes/raw-json-retention-policy/tasks.md
git add internal/ingest/ingest.go internal/ingest/raw_retention_test.go internal/cli/prune.go internal/cli/index.go internal/cli/mcp.go internal/cli/raw_retention_test.go openspec/changes/raw-json-retention-policy/tasks.md
git commit -m "index: sweep raw_json of sessions that aged past the retention window"
```

---

## Task 6: Residual check, restore path, help text (§3.4)

**Files:**
- Modify: `internal/cli/index.go` — `checkPrunedResiduals` `:120-154` (signature, doc comment, per-uuid query `:137-145`); caller (Task 5's `checkPrunedResiduals(..., st.StillChanging)` line); `--full` flag usage `:105`
- Modify: `internal/cli/prune.go:34-38` (`Long`)
- Modify: `internal/cli/index_settle_test.go:61`, `:69` (pass `0`)
- Modify: `internal/cli/raw_retention_test.go` (append)

**Interfaces:**
- Consumes: `Stats.RawCutoff` (Task 5), `runConfigCmd` (Task 3), `mustPrune`, `pruneSandbox`, `runIndexCmd`, `reopen`, `rawJSONOf`.
- Produces: `func checkPrunedResiduals(ctx context.Context, database *db.DB, stderr io.Writer, stillChanging []string, rawCutoff int64) error` — skips sessions with `COALESCE(ended_at, started_at, 0) < rawCutoff` when `rawCutoff > 0`.

- [ ] **Step 1: Write the failing behaviour tests**

Append to `internal/cli/raw_retention_test.go`:

````go
// §3.4 restore path: with retention set, --full blanks the old session and exits
// 0; after `config raw-retention off`, --full restores it.
func TestRetentionOffThenFullRestores(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "14d")
	database.Close()
	if out, err := runIndexCmd(t, "--full"); err != nil {
		t.Fatalf("index --full with retention: %v\n%s", err, out)
	}
	d := reopen(t)
	if rawJSONOf(t, d, "sess-old")[0] != "" {
		t.Fatal("--full kept raw_json of a session outside the window")
	}
	d.Close()
	if _, err := runConfigCmd(t, "raw-retention", "off"); err != nil {
		t.Fatal(err)
	}
	if out, err := runIndexCmd(t, "--full"); err != nil {
		t.Fatalf("index --full after off: %v\n%s", err, out)
	}
	d = reopen(t)
	defer d.Close()
	if rawJSONOf(t, d, "sess-old")[0] == "" {
		t.Fatal("retention off + --full did not restore raw_json")
	}
}

func TestHelpTextNamesRetentionRestorePath(t *testing.T) {
	const hint = "clio config raw-retention off"
	if !strings.Contains(newPruneRawCmd().Long, hint) {
		t.Errorf("prune-raw help lacks %q", hint)
	}
	if f := newIndexCmd().Flags().Lookup("full"); f == nil || !strings.Contains(f.Usage, hint) {
		t.Errorf("index --full help lacks %q", hint)
	}
}
````

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/cli/ -run 'TestRetentionOffThenFullRestores|TestHelpTextNamesRetention' -count=1`
Expected:
```
--- FAIL: TestRetentionOffThenFullRestores
    raw_retention_test.go:217: index --full with retention: 1 pruned session(s) could not be restored
--- FAIL: TestHelpTextNamesRetentionRestorePath
    raw_retention_test.go:240: prune-raw help lacks "clio config raw-retention off"
    raw_retention_test.go:243: index --full help lacks "clio config raw-retention off"
```

- [ ] **Step 3: Write the failing residual unit test and update the existing callers**

Append to `internal/cli/raw_retention_test.go`:

````go
// §3.4: sessions the active retention says must be blank are not residuals; a
// pruned session inside the window still is, and so is any pruned session when
// retention is off.
func TestCheckPrunedResidualsExcludesRetentionBlanked(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30) // last activity 30 days ago
	mustPrune(t, database, time.Now().Add(-14*24*time.Hour).Unix(), "sess-old")
	var errOut bytes.Buffer
	cutoff14 := time.Now().Add(-14 * 24 * time.Hour).Unix()
	if err := checkPrunedResiduals(context.Background(), database, &errOut, nil, cutoff14); err != nil {
		t.Fatalf("retention 14d: err=%v for a session it must blank\n%s", err, errOut.String())
	}
	errOut.Reset()
	cutoff60 := time.Now().Add(-60 * 24 * time.Hour).Unix()
	if err := checkPrunedResiduals(context.Background(), database, &errOut, nil, cutoff60); err == nil || !strings.Contains(errOut.String(), "sess-old") {
		t.Fatalf("retention 60d: err=%v stderr=%q; want the in-window session reported", err, errOut.String())
	}
	errOut.Reset()
	if err := checkPrunedResiduals(context.Background(), database, &errOut, nil, 0); err == nil || !strings.Contains(errOut.String(), "sess-old") {
		t.Fatalf("retention off: err=%v stderr=%q; want the residual reported", err, errOut.String())
	}
}
````

Apply to `internal/cli/index_settle_test.go`:

````diff
diff --git a/internal/cli/index_settle_test.go b/internal/cli/index_settle_test.go
index de2985c..3ead4eb 100644
--- a/internal/cli/index_settle_test.go
+++ b/internal/cli/index_settle_test.go
@@ -58,7 +58,7 @@ func TestCheckPrunedResidualsSkipsStillChangingFiles(t *testing.T) {
 	mustPrune(t, database, time.Now().Add(-14*24*time.Hour).Unix(), "sess-old")
 
 	var errOut bytes.Buffer
-	if err := checkPrunedResiduals(context.Background(), database, &errOut, []string{path}); err != nil {
+	if err := checkPrunedResiduals(context.Background(), database, &errOut, []string{path}, 0); err != nil {
 		t.Errorf("err = %v for a session whose file is still changing\n%s", err, errOut.String())
 	}
 	if strings.Contains(errOut.String(), "sess-old") {
@@ -66,7 +66,7 @@ func TestCheckPrunedResidualsSkipsStillChangingFiles(t *testing.T) {
 	}
 
 	errOut.Reset()
-	err := checkPrunedResiduals(context.Background(), database, &errOut, []string{"/elsewhere/other.jsonl"})
+	err := checkPrunedResiduals(context.Background(), database, &errOut, []string{"/elsewhere/other.jsonl"}, 0)
 	if err == nil || !strings.Contains(err.Error(), "could not be restored") || !strings.Contains(errOut.String(), "sess-old") {
 		t.Errorf("err = %v, stderr = %q; want the residual reported", err, errOut.String())
 	}
````

Run: `go test ./internal/cli/ -run TestCheckPrunedResiduals -count=1`
Expected: build failure — `too many arguments in call to checkPrunedResiduals` with `have (context.Context, *db.DB, *bytes.Buffer, []string, number)` / `want (context.Context, *db.DB, io.Writer, []string)`.

- [ ] **Step 4: Implement**

Apply to `internal/cli/index.go` and `internal/cli/prune.go`:

````diff
diff --git a/internal/cli/index.go b/internal/cli/index.go
index 641bf59..c779147 100644
--- a/internal/cli/index.go
+++ b/internal/cli/index.go
@@ -100,7 +100,7 @@ func newIndexCmd() *cobra.Command {
 				}
 			}
 			if full {
-				if err := checkPrunedResiduals(cmd.Context(), database, os.Stderr, st.StillChanging); err != nil {
+				if err := checkPrunedResiduals(cmd.Context(), database, os.Stderr, st.StillChanging, st.RawCutoff); err != nil {
 					return errors.Join(err, sweepErr)
 				}
 			}
@@ -110,7 +110,7 @@ func newIndexCmd() *cobra.Command {
 			return nil
 		},
 	}
-	cmd.Flags().BoolVar(&full, "full", false, "Force a full re-index instead of incremental")
+	cmd.Flags().BoolVar(&full, "full", false, "Force a full re-index instead of incremental; restores pruned raw_json (with a raw retention set, run 'clio config raw-retention off' first)")
 	cmd.Flags().BoolVar(&purgeMissing, "purge-missing", false, "Delete indexed sessions whose source transcript no longer exists (kept by default)")
 	cmd.Flags().BoolVar(&force, "force", false, "With --purge-missing: delete even when many transcripts went missing together (normally refused as a likely unmounted directory)")
 	return cmd
@@ -130,8 +130,10 @@ func optimizeFTS(ctx context.Context, d *db.DB) error {
 // session whose file became unreadable/undiscoverable would stay pruned
 // silently — surface it with a non-zero exit rather than reporting a clean
 // success. Sessions whose file was still changing are left out: the summary
-// already names those files, and they are restored once the file is idle.
-func checkPrunedResiduals(ctx context.Context, database *db.DB, stderr io.Writer, stillChanging []string) error {
+// already names those files, and they are restored once the file is idle. So are
+// sessions older than rawCutoff (0 = retention off), which the active raw
+// retention keeps blank on purpose.
+func checkPrunedResiduals(ctx context.Context, database *db.DB, stderr io.Writer, stillChanging []string, rawCutoff int64) error {
 	all, err := sessions.PrunedResiduals(ctx, database)
 	if err != nil {
 		return fmt.Errorf("pruned-residual check failed: %w", err)
@@ -143,10 +145,14 @@ func checkPrunedResiduals(ctx context.Context, database *db.DB, stderr io.Writer
 	var residual []string
 	for _, u := range all {
 		var src string
-		err := database.QueryRowContext(ctx, `SELECT source_file FROM sessions WHERE uuid = ?`, u).Scan(&src)
+		var last int64
+		err := database.QueryRowContext(ctx, `SELECT source_file, COALESCE(ended_at, started_at, 0) FROM sessions WHERE uuid = ?`, u).Scan(&src, &last)
 		if err != nil && !errors.Is(err, sql.ErrNoRows) {
 			return fmt.Errorf("pruned-residual check failed: %w", err)
 		}
+		if err == nil && rawCutoff > 0 && last < rawCutoff {
+			continue // blank by retention, not a failed restore
+		}
 		if !busy[src] {
 			residual = append(residual, u)
 		}
diff --git a/internal/cli/prune.go b/internal/cli/prune.go
index f1d714e..2af6f21 100644
--- a/internal/cli/prune.go
+++ b/internal/cli/prune.go
@@ -35,7 +35,11 @@ func newPruneRawCmd() *cobra.Command {
 sessions older than --older-than whose raw form is restorable from the source
 file. This is a reversible cache eviction: 'clio index --full' re-derives the
 raw form. Search, content, usage, and activity data are untouched. Space is
-reclaimed on disk only with --vacuum (or a later VACUUM).`,
+reclaimed on disk only with --vacuum (or a later VACUUM).
+
+With a raw retention window set ('clio config raw-retention'), 'clio index'
+prunes the same way on its own, and --full keeps old sessions blank: run
+'clio config raw-retention off' before 'clio index --full' to restore them.`,
 		RunE: func(cmd *cobra.Command, args []string) error {
 			if err := validateSource(source); err != nil {
 				return err
````

The flag usage uses single quotes on purpose: pflag turns a back-quoted word in a usage string into the flag's value name.

- [ ] **Step 5: Run the tests and the package**

Run: `go test ./internal/cli/ -run 'TestCheckPrunedResiduals|TestRetentionOffThenFullRestores|TestHelpTextNamesRetention|TestPrune' -count=1`
Expected: `ok`.
Run: `go test ./internal/cli/ -count=1`
Expected: `ok`.
Run: `d=$(mktemp -d) && go build -o "$d/clio" ./cmd/clio && "$d/clio" index --help | grep -- '--full'`
Expected: `--full   Force a full re-index instead of incremental; restores pruned raw_json (with a raw retention set, run 'clio config raw-retention off' first)` (no value name between `--full` and the text).

- [ ] **Step 6: Tick `tasks.md` 4.2, then commit**

```bash
sed -i '' 's/^- \[ \] 4.2 /- [x] 4.2 /' openspec/changes/raw-json-retention-policy/tasks.md
git add internal/cli/index.go internal/cli/prune.go internal/cli/index_settle_test.go internal/cli/raw_retention_test.go openspec/changes/raw-json-retention-policy/tasks.md
git commit -m "index: retention-blanked sessions are not unrestored residuals"
```

---

## Task 7: doctor line and show note (§3.5, §4)

**Files:**
- Modify: `internal/doctor/doctor.go` — imports `:6-21`; insert after the pruned-raw block `:304`
- Create: `internal/doctor/raw_retention_test.go`
- Modify: `internal/cli/show.go` — after `:51`; calls `:100`, `:113`; `prunedNote` `:188-192`
- Modify: `internal/cli/raw_retention_test.go` (append)

**Interfaces:**
- Consumes: `db.(*DB).RawRetention`, `db.(*DB).PutSetting` (Task 2); `findResult`, `Run`; `pruneSandbox`, `mustPrune`, `putRetention`, `runShowCmd`.
- Produces: doctor result `{Name: "raw retention", OK, Detail}` immediately after `pruned raw_json`; `func prunedNote(w io.Writer, sessionID, retention string)`.

- [ ] **Step 1: Write the failing tests**

`internal/doctor/raw_retention_test.go`:

````go
package doctor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/db"
)

func TestRunReportsRawRetention(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if r := findResult(t, Run(d, dir, dbPath), "raw retention"); !r.OK || r.Detail != "off" {
		t.Fatalf("unset: %+v, want ok \"off\"", r)
	}
	d.PutSetting(ctx, db.RawRetentionKey, "14d")
	if r := findResult(t, Run(d, dir, dbPath), "raw retention"); !r.OK || r.Detail != "14d" {
		t.Fatalf("14d: %+v, want ok \"14d\"", r)
	}
	d.PutSetting(ctx, db.RawRetentionKey, "abc")
	if r := findResult(t, Run(d, dir, dbPath), "raw retention"); r.OK || !strings.Contains(r.Detail, `"abc"`) {
		t.Fatalf("bad value: %+v, want a warning naming \"abc\"", r)
	}
	// A database last migrated by an older clio (doctor opens read-only and does
	// not migrate) has no settings table: retention is off there.
	if _, err := d.Exec(`ALTER TABLE settings RENAME TO settings_from_the_future`); err != nil {
		t.Fatal(err)
	}
	if r := findResult(t, Run(d, dir, dbPath), "raw retention"); !r.OK || r.Detail != "off" {
		t.Fatalf("no settings table: %+v, want ok \"off\"", r)
	}
}

// The retention line sits next to the pruned raw_json line.
func TestRawRetentionFollowsPrunedLine(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	results := Run(d, dir, dbPath)
	for i, r := range results {
		if r.Name == "pruned raw_json" {
			if i+1 >= len(results) || results[i+1].Name != "raw retention" {
				t.Fatalf("result after %q is not raw retention: %+v", r.Name, results[i+1:])
			}
			return
		}
	}
	t.Fatal("pruned raw_json line missing")
}
````

Append to `internal/cli/raw_retention_test.go`:

````go
func TestPrunedNoteMentionsRetention(t *testing.T) {
	var b bytes.Buffer
	prunedNote(&b, "abcd1234", "")
	if got, want := b.String(), "note: raw form of session abcd1234 was pruned to save space — restore it with `clio index --full`\n"; got != want {
		t.Fatalf("retention off: %q, want the unchanged note %q", got, want)
	}
	b.Reset()
	prunedNote(&b, "abcd1234", "14d")
	if !strings.Contains(b.String(), "raw retention is 14d") || !strings.Contains(b.String(), "clio config raw-retention off") {
		t.Fatalf("retention 14d: %q lacks the retention restore hint", b.String())
	}
}

func TestShowRawNoteNamesRetention(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	mustPrune(t, database, time.Now().Add(-14*24*time.Hour).Unix(), "sess-old")
	putRetention(t, database, "14d")
	database.Close()
	oldErr := os.Stderr
	rerr, werr, _ := os.Pipe()
	os.Stderr = werr
	runShowCmd(t, "sess-old", "--format", "raw", "--source", "all")
	werr.Close()
	os.Stderr = oldErr
	var eb bytes.Buffer
	eb.ReadFrom(rerr)
	if !strings.Contains(eb.String(), "raw retention is 14d") {
		t.Fatalf("raw show note does not mention the retention setting:\n%s", eb.String())
	}
}
````

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/doctor/ -run 'TestRunReportsRawRetention|TestRawRetentionFollowsPrunedLine' -count=1`
Expected: `result "raw retention" not found in [...]` and `result after "pruned raw_json" is not raw retention: [{Name:fts bloat …`.
Run: `go test ./internal/cli/ -run 'TestPrunedNote|TestShowRawNote' -count=1`
Expected: build failure — `internal/cli/raw_retention_test.go:271:17: too many arguments in call to prunedNote` / `have (*bytes.Buffer, string, string)` / `want (string)`.

- [ ] **Step 3: Implement**

Apply to `internal/doctor/doctor.go` and `internal/cli/show.go`:

````diff
diff --git a/internal/cli/show.go b/internal/cli/show.go
index 9293785..05c6e43 100644
--- a/internal/cli/show.go
+++ b/internal/cli/show.go
@@ -49,6 +49,11 @@ func newShowCmd() *cobra.Command {
 				return resolveError(err)
 			}
 			format = resolveShowFormat(format, jsonFlag)
+			// An unreadable setting only drops the retention hint from the note.
+			_, retention, rerr := database.RawRetention(cmd.Context())
+			if rerr != nil {
+				retention = ""
+			}
 			if limit <= 0 {
 				limit = defaultShowMessages
 			}
@@ -97,7 +102,7 @@ func newShowCmd() *cobra.Command {
 					return err
 				}
 				if sawPruned {
-					prunedNote(shortID(sess.UUID))
+					prunedNote(os.Stderr, shortID(sess.UUID), retention)
 				}
 				if includeSubagents {
 					for _, c := range children {
@@ -110,7 +115,7 @@ func newShowCmd() *cobra.Command {
 							return err
 						}
 						if sp {
-							prunedNote(shortID(c.UUID)) // per-session, not one aggregate note
+							prunedNote(os.Stderr, shortID(c.UUID), retention) // per-session, not one aggregate note
 						}
 					}
 				}
@@ -185,10 +190,15 @@ func writeRaw(w io.Writer, msgs []sessions.Message) (sawPruned bool, err error)
 	return sawPruned, nil
 }
 
-// prunedNote prints a per-session note (to stderr) that a session's raw form
-// was pruned and how to restore it.
-func prunedNote(sessionID string) {
-	fmt.Fprintf(os.Stderr, "note: raw form of session %s was pruned to save space — restore it with `clio index --full`\n", sessionID)
+// prunedNote prints a per-session note that a session's raw form was pruned and
+// how to restore it. With a raw retention window set, --full alone keeps old
+// sessions blank, so the hint turns retention off first.
+func prunedNote(w io.Writer, sessionID, retention string) {
+	if retention == "" {
+		fmt.Fprintf(w, "note: raw form of session %s was pruned to save space — restore it with `clio index --full`\n", sessionID)
+		return
+	}
+	fmt.Fprintf(w, "note: raw form of session %s was pruned to save space (raw retention is %s) — restore it with `clio config raw-retention off`, then `clio index --full`\n", sessionID, retention)
 }
 
 // anyPruned reports whether any message has a blanked (pruned) raw_json.
diff --git a/internal/doctor/doctor.go b/internal/doctor/doctor.go
index 44dd3a7..1af4a66 100644
--- a/internal/doctor/doctor.go
+++ b/internal/doctor/doctor.go
@@ -5,6 +5,7 @@ package doctor
 
 import (
 	"bytes"
+	"context"
 	"database/sql"
 	"errors"
 	"fmt"
@@ -303,6 +304,21 @@ func RunWith(database *db.DB, projectsDir, dbPath string, opts Options) []Result
 			prunedMsgs, float64(freelist*pageSize)/1e6))
 	}
 
+	// Raw retention setting (clio config raw-retention). A database migrated by
+	// an older clio has no settings table; doctor does not migrate, so that is off.
+	switch _, stored, rerr := database.RawRetention(context.Background()); {
+	case rerr != nil && strings.Contains(rerr.Error(), "no such table"):
+		add("raw retention", true, "off")
+	case rerr != nil && stored != "":
+		add("raw retention", false, fmt.Sprintf("stored value %q is invalid (%v); ingest keeps raw_json — fix with `clio config raw-retention off` or a window such as 14d", stored, rerr))
+	case rerr != nil:
+		add("raw retention", false, rerr.Error())
+	case stored == "":
+		add("raw retention", true, "off")
+	default:
+		add("raw retention", true, stored)
+	}
+
 	if opts.Quick {
 		skip("fts bloat")
 	} else {
````

- [ ] **Step 4: Run the tests and the packages**

Run: `go test ./internal/doctor/ ./internal/cli/ -count=1`
Expected: both `ok` (includes the existing `TestShowRawFormatPrunedNote` and `TestShowCommandOnPrunedSession`, which pin the unchanged note when retention is off, and the doctor quick/full same-checks tests).

- [ ] **Step 5: Tick `tasks.md` 5.1, then commit**

```bash
sed -i '' 's/^- \[ \] 5.1 /- [x] 5.1 /' openspec/changes/raw-json-retention-policy/tasks.md
git add internal/doctor/doctor.go internal/doctor/raw_retention_test.go internal/cli/show.go internal/cli/raw_retention_test.go openspec/changes/raw-json-retention-policy/tasks.md
git commit -m "doctor, show: surface the raw retention setting"
```

---

## Task 8: READMEs, usage guide, and a measurement on the real index (§3.4, documentation spec, 已裁決 #1)

**Files:**
- Modify: `README.md:79` (insert one line after it)
- Modify: `README_zh-TW.md:78` (insert one line after it)
- Modify: `docs/USAGE.md:366` (insert a section before `### \`clio index\``) and `:530` (insert one cheat-sheet line after it)

**Interfaces:**
- Consumes: the command and messages from Tasks 3–6.
- Produces: user documentation; no code.

- [ ] **Step 1: Write the check that must fail**

Save as a one-off command (not committed):

```bash
check_docs() {
  local fail=0 f
  for f in README.md README_zh-TW.md docs/USAGE.md; do
    grep -q 'clio config raw-retention 14d' "$f" || { echo "$f: no 'clio config raw-retention 14d'"; fail=1; }
    grep -q 'clio config raw-retention off' "$f" || { echo "$f: no restore path 'clio config raw-retention off'"; fail=1; }
  done
  grep -q '^### `clio config raw-retention' docs/USAGE.md || { echo "docs/USAGE.md: no reference entry"; fail=1; }
  return $fail
}
check_docs; echo "exit=$?"
```

- [ ] **Step 2: Run it and see it fail**

Expected: seven `no …` lines (two per file plus the missing reference entry) and `exit=1`.

- [ ] **Step 3: Edit the docs**

````diff
diff --git a/README.md b/README.md
index fdb1e92..5d25279 100644
--- a/README.md
+++ b/README.md
@@ -77,6 +77,7 @@ clio file-history internal/search/rank.go  # which past sessions read/edited thi
 clio doctor                     # health check, plus hook / MCP / lease / index-freshness checks
 clio doctor --quick             # skips the whole-database scans (db integrity, FTS content check, FTS bloat); FTS row counts still compared
 clio prune-raw --older-than 30d --vacuum   # reclaim space: blank restorable old raw_json (reversible via index --full)
+clio config raw-retention 14d   # keep raw_json only for sessions active in the last 14 days, from now on (restore: clio config raw-retention off, then clio index --full)
 ```
 
 To remove the integration later: `clio uninstall-mcp`.
diff --git a/README_zh-TW.md b/README_zh-TW.md
index d342444..7518890 100644
--- a/README_zh-TW.md
+++ b/README_zh-TW.md
@@ -76,6 +76,7 @@ clio file-history internal/search/rank.go  # 哪些過去的 session 讀過／
 clio doctor                     # 健康檢查，另含 hook／MCP／lease／索引新鮮度檢查
 clio doctor --quick             # 略過全庫掃描（db integrity、FTS 內容檢查、FTS bloat）；FTS 筆數比對照常執行
 clio prune-raw --older-than 30d --vacuum   # 回收空間：清空可還原的舊 raw_json（用 index --full 可復原）
+clio config raw-retention 14d   # 之後只保留最近 14 天內有活動的 session 的 raw_json（還原：clio config raw-retention off，再 clio index --full）
 ```
 
 之後要移除整合：`clio uninstall-mcp`。
diff --git a/docs/USAGE.md b/docs/USAGE.md
index d7c2761..45cf40d 100644
--- a/docs/USAGE.md
+++ b/docs/USAGE.md
@@ -363,6 +363,29 @@ instead of blank lines; `--format markdown` is unaffected.
 一律 fail-closed）。prune 過的 session 跑 `clio show --format raw|json` 會印提示與
 還原指令而非空行；`--format markdown` 不受影響。
 
+### `clio config raw-retention` — keep raw_json for a rolling window / 滾動保留 raw_json
+
+Set once, and clio keeps `raw_json` only for sessions whose last activity is
+inside the window: every ingest (index, `--full`, MCP, watcher) stores older
+sessions without it, and `clio index` blanks sessions that aged past it, with
+the same restorability checks as `clio prune-raw`. Search, content, usage and
+activity data are untouched. The window rolls, so a date is rejected. The file
+shrinks on disk only with `clio prune-raw --vacuum`. A stored value that no
+longer parses is treated as off (raw kept); `clio index` and `clio doctor` warn.
+
+設定一次後，clio 只為「最後活動在視窗內」的 session 保留 `raw_json`：每次 ingest
+（index、`--full`、MCP、watcher）寫入較舊的 session 時就不存，`clio index` 結尾也會把
+之後才變舊的 session 清空（可還原檢查與 `clio prune-raw` 相同）。搜尋、內容、用量、
+活動資料都不動。視窗會滾動，所以不接受日期。檔案要 `clio prune-raw --vacuum` 才會縮小。
+存的值若無法解析就當作關閉（保留 raw），`clio index` 與 `clio doctor` 會警告。
+
+```bash
+clio config raw-retention          # print the window, or off / 印出目前設定或 off
+clio config raw-retention 14d      # keep 14 days / 保留 14 天
+clio config raw-retention off      # turn off / 關閉
+clio index --full                  # after off: restore raw_json / 關閉後還原 raw_json
+```
+
 ### `clio index` — (re)build the index / 建立索引
 
 | Flag | English | 中文 |
@@ -528,6 +551,7 @@ clio list [--since 7d] [--project X] [--min-turns N] [--touched P] [--tool T] [-
 clio show <uuid-or-prefix> [--format markdown|json|raw] [--no-tool-output]
 clio activity --by file|command|tool|pattern|url [--since 7d] [--project X]
 clio index [--full]                              # (re)index manually
+clio config raw-retention [14d|off]              # rolling raw_json retention (restore: clio config raw-retention off, then clio index --full)
 clio recall [--tail-runes N]                     # recent-activity digest (current project), headed by the last session's closing message
 clio file-history <path> [--since 7d] [--limit N] # sessions that touched a file, newest first
 clio doctor                                      # health check
````

- [ ] **Step 4: Run the check again**

Run: `check_docs; echo "exit=$?"`
Expected: no output lines and `exit=0`.
Run: `OPENSPEC_TELEMETRY=0 openspec validate raw-json-retention-policy --strict`
Expected: `Change 'raw-json-retention-policy' is valid`.

- [ ] **Step 5: Measure on a copy of the real index (report numbers only; no threshold)**

The real DB is read only: never open it with `clio` and never write next to it. Copy it with SQLite's online backup (it is in WAL mode and `clio mcp` may be writing) into the implementing session's scratchpad, and point `XDG_DATA_HOME` at the copy so `clio` resolves its DB there (`internal/config/config.go:51-53`). Transcripts are read from the real `~/.claude/projects` and `~/.codex/sessions`, read only, as any `clio index` does.

```bash
SCRATCH="<this session's scratchpad directory>"   # e.g. the path the harness lists as the scratchpad
SRC="$HOME/Library/Application Support/clio/db.sqlite"
mkdir -p "$SCRATCH/measure/clio"
sqlite3 -readonly "$SRC" ".backup '$SCRATCH/measure/clio/db.sqlite'"
go build -o "$SCRATCH/measure/clio-bin" ./cmd/clio
export XDG_DATA_HOME="$SCRATCH/measure"
B="$SCRATCH/measure/clio-bin"
"$B" config raw-retention off
/usr/bin/time -p "$B" index --full        # A: --full, retention off (restores raw everywhere)
/usr/bin/time -p "$B" index               # B: incremental, retention off (baseline)
"$B" config raw-retention 14d
/usr/bin/time -p "$B" index               # C: incremental + sweep that prunes everything older than 14d
/usr/bin/time -p "$B" index               # D: incremental + sweep with nothing left to prune (steady state)
/usr/bin/time -p "$B" index --full        # E: --full, retention 14d
ls -l "$SCRATCH/measure/clio/db.sqlite"
unset XDG_DATA_HOME
```

Report `real` for A–E, the sweep summary line C printed, sweep cost as C − B and D − B, and the DB file size. If `index --full` says `clio mcp holds the index lock`, `XDG_DATA_HOME` was not exported (the copy has its own lock path). Delete `$SCRATCH/measure` afterwards only if the user agrees; it is about 3 GB.

- [ ] **Step 6: Tick `tasks.md` 5.2, run everything, then commit**

```bash
sed -i '' 's/^- \[ \] 5.2 /- [x] 5.2 /' openspec/changes/raw-json-retention-policy/tasks.md
gofmt -l internal
go vet ./...
go test ./... -count=1
git add README.md README_zh-TW.md docs/USAGE.md openspec/changes/raw-json-retention-policy/tasks.md
git commit -m "docs: raw retention setting and restore path"
```
Expected before the commit: `gofmt -l` prints nothing, `go vet` prints nothing, every package `ok`.

After Task 8 the design's workflow (§6) continues outside this plan: codex review of the real diff, PR, merge after user approval, then the release PR (archive, CHANGELOG, version bump in both READMEs).

---

## Spec item → Task

| Design item | Task(s) | Tests |
|---|---|---|
| §3.1 settings table (migration) | 2 | `TestMigration0017Settings` |
| §3.1 `clio config raw-retention` get / set / off | 3 | `TestConfigRawRetentionRoundTrip`, `TestRootRegistersConfigRawRetention` |
| §3.1 relative durations only; reject date, zero, negative, garbage, same wording style as prune-raw | 2, 3 | `TestParseRetentionRejectsDatesZeroNegativeGarbage`, `TestConfigRawRetentionRejectsBadValues`, `TestConfigRawRetentionHelpStillWorks` |
| §3.1 default unset = today's behaviour | 4 | `TestRawRetentionUnsetKeepsRaw`, `TestIngestAllReportsRawCutoff` (unset half), whole existing suite |
| §3.1 cutoff computed once per run | 4, 5 | `rawCutoff` called once per `IngestAll`/`IngestFile`; `TestIngestAllReportsRawCutoff` |
| §3.2 drop at commit, same age expression as prune-raw, rows of this commit only | 4 | `TestRawRetentionFullReindexBlanksOldKeepsRecent`, `TestRawRetentionIncrementalNewOldFileBlanks`, `TestRawRetentionLeavesEarlierCommitsAlone` |
| §3.2 every ingest path and every source | 4, 5 | no call-site change needed (fact 8); `TestRawRetentionBlanksOldCodexRollout` (Codex); `TestReadCatchUpsDoNotSweep` and `TestMCPStartupCatchUpSweeps` run the MCP catch-ups over real ingest |
| §3.2 content, FTS, tool calls/targets, usage unchanged | 4 | `TestRawRetentionFullReindexBlanksOldKeepsRecent` |
| §3.2 resumed old session keeps new raw | 4 | `TestRawRetentionAppendMakesSessionRecent` |
| §3.2 activity from parsed events; repair tolerates blank rows | 4 | `TestRawRetentionActivityFromParseAndRepairTolerates` |
| §3.3 sweep in `clio index` (incremental and `--full`), prune-raw machinery | 5 | `TestIndexSweepPrunesAgedSession`, `TestRetentionOffThenFullRestores` (`--full` path) |
| §3.3 skipped sessions intact, skips not warnings | 5 | `TestIndexSweepSkipsMissingSource`, `TestSweepRawRetentionSkipsChangedSource` |
| §3.3 sweep only in `clio index` — amended 2026-11-07: also once at MCP leader startup; per-read catch-ups never | 5 | `TestReadCatchUpsDoNotSweep`, `TestMCPStartupCatchUpSweeps`, `TestMCPStartupSweepFailureOnlyLogs` |
| §3.3 one summary line when pruned | 5 | `TestIndexSweepPrunesAgedSession` |
| §3.4 restore = off + `--full`; help of prune-raw and `index --full` | 6 | `TestRetentionOffThenFullRestores`, `TestHelpTextNamesRetentionRestorePath` |
| §3.4 both READMEs state the restore path | 8 | `check_docs` |
| §3.4 residual check excludes retention-blanked, still reports genuine | 6 | `TestCheckPrunedResidualsExcludesRetentionBlanked`, existing `TestCheckPrunedResidualsSkipsStillChangingFiles` |
| §3.5 doctor `raw retention` line next to pruned line | 7 | `TestRunReportsRawRetention`, `TestRawRetentionFollowsPrunedLine` |
| §3.5 show note mentions retention | 7 | `TestPrunedNoteMentionsRetention`, `TestShowRawNoteNamesRetention` |
| §4 unreadable/invalid setting → off, ingest keeps raw | 2, 4 | `TestRawRetention` (db), `TestRawRetentionBadStoredValueFailsOpen` |
| §4 `clio index` warns naming the value | 4 | `TestIndexWarnsOnBadRetentionAndKeepsRaw` |
| §4 `clio doctor` warns naming the value | 7 | `TestRunReportsRawRetention` (bad value) |
| §4 sweep failure: ingest stays committed, non-zero exit | 5 | `TestIndexSweepFailureExitsNonZeroAfterIngest` |
| §5 every listed test | 2–7 | rows above |
| §2 non-goals (no VACUUM, no config file/env, quota_snapshots untouched) | all | Global Constraints; no Task adds them |
| §6 workflow: openspec `validate --strict` | 1, 8 | `openspec validate … --strict` |
| 已裁決 #1: measure `--full` and sweep cost on a copy of the real DB | 8 | Step 5 (numbers only) |

---

## 查證

Existing symbols this plan calls or edits, with the grep that found them (run 2026-11-07 on branch `raw-retention`, HEAD `7527cb8`):

```
internal/watcher/watcher.go:42:func New(ing *ingest.Ingester, projectsDir string, log *slog.Logger) *Watcher {
internal/watcher/watcher.go:47:func (w *Watcher) Run(ctx context.Context) error {
internal/timeutil/timeutil.go:15:func ParseSince(s string) (int64, error) {
internal/ingest/activity.go:92:func (ing *Ingester) BackfillActivity(ctx context.Context) error {
internal/ingest/activity.go:98:func (ing *Ingester) RepairActivity(ctx context.Context) error {
internal/ingest/activity.go:102:func (ing *Ingester) backfillActivity(ctx context.Context, repair bool) error {
internal/ingest/activity.go:249:func toolUseBlocks(raw string) []model.ContentBlock {
internal/ingest/ingest.go:62:func New(database *db.DB, log *slog.Logger) *Ingester {
internal/ingest/ingest.go:75:func NewWithBuiltinSources(database *db.DB, log *slog.Logger) *Ingester {
internal/ingest/ingest.go:101:func (ing *Ingester) IngestAll(ctx context.Context, projectsDir string, force bool) (Stats, error) {
internal/ingest/ingest.go:203:func (ing *Ingester) IngestFile(ctx context.Context, path string, force bool) (int, bool, error) {
internal/ingest/ingest.go:210:func (ing *Ingester) ingestOnce(ctx context.Context, path string, force bool) (n int, ingested, stale bool, err error) {
internal/ingest/ingest.go:218:func (ing *Ingester) ingestFile(ctx context.Context, path string, force bool) (int, bool, error) {
internal/ingest/ingest.go:512:func (ing *Ingester) commit(kind changeKind, sess model.Session, msgs []model.Message, excludedToolUses []string, usage *usageResult, fs FileState) (int, error) {
internal/ingest/ingest.go:664:func (ing *Ingester) upsertIngestState(tx *sql.Tx, fs FileState, monotonic bool) error {
internal/ingest/ingest.go:714:func (ing *Ingester) upsertSession(tx *sql.Tx, s model.Session, userTurns int, kind changeKind) error {
internal/ingest/source.go:128:func (ing *Ingester) ExtraRoots() []string { return ing.extraRoots() }
internal/doctor/doctor.go:64:func Run(database *db.DB, projectsDir, dbPath string) []Result {
internal/doctor/doctor.go:69:func RunWith(database *db.DB, projectsDir, dbPath string, opts Options) []Result {
internal/cli/index.go:22:func newIndexCmd() *cobra.Command {
internal/cli/index.go:115:func optimizeFTS(ctx context.Context, d *db.DB) error {
internal/cli/index.go:126:func checkPrunedResiduals(ctx context.Context, database *db.DB, stderr io.Writer, stillChanging []string) error {
internal/cli/index.go:159:func printIndexSummary(stdout, stderr io.Writer, st ingest.Stats) {
internal/cli/index.go:173:func runIndexIngest(ctx context.Context, ing interface {
internal/cli/prune.go:22:func newPruneRawCmd() *cobra.Command {
internal/cli/prune.go:78:func runPruneRaw(ctx context.Context, o pruneOpts) error {
internal/cli/prune.go:168:func classifyPrunable(c sessions.PruneCandidate, roots []string, pending map[string]bool) string {
internal/cli/prune.go:201:func underAnyRoot(path string, roots []string) bool {
internal/cli/prune.go:214:func printPruneSummary(o pruneOpts, candidates, eligible int, msgs, removed int64, skips map[string]int) {
internal/cli/prune.go:249:func humanBytes(n int64) string {
internal/cli/common.go:67:func stderrLogger() *slog.Logger {
internal/cli/common.go:71:func discardLogger() *slog.Logger {
internal/cli/common.go:79:func openAndCatchUp() (*db.DB, error) {
internal/cli/common.go:108:func runCatchUp(ctx context.Context, ing leaderIngester, projects string, log *slog.Logger) {
internal/cli/common.go:114:func runIngestBackfill(ctx context.Context, ing leaderIngester, projects string, full, catchUp bool, backfill func(context.Context) error, log *slog.Logger) (ingest.Stats, error) {
internal/cli/show.go:24:func newShowCmd() *cobra.Command {
internal/cli/show.go:169:func writeRaw(w io.Writer, msgs []sessions.Message) (sawPruned bool, err error) {
internal/cli/show.go:190:func prunedNote(sessionID string) {
internal/cli/show.go:195:func anyPruned(msgs []sessions.Message) bool {
internal/cli/mcp.go:334:func startupCatchup(wctx context.Context, leaderFlag *atomic.Bool, env *leaderEnv, projects string, log *slog.Logger, preHeld bool) bool {
internal/cli/root.go:16:func newRootCmd() *cobra.Command {
internal/cli/doctor.go:18:func reportDoctor(w io.Writer, results []doctor.Result) error {
internal/sessions/prune.go:30:func PruneCandidates(ctx context.Context, database *db.DB, cutoff int64, source, project string) ([]PruneCandidate, error) {
internal/sessions/prune.go:62:func PendingBackfillSessions(ctx context.Context, database *db.DB) (map[string]bool, error) {
internal/sessions/prune.go:113:func PruneRawJSON(ctx context.Context, database *db.DB, cutoff int64, source, project string, pairs []PrunePair, dryRun bool) (PruneResult, error) {
internal/sessions/prune.go:193:func PrunedResiduals(ctx context.Context, database *db.DB) ([]string, error) {
internal/db/db.go:37:func Open(path string) (*DB, error) {
internal/db/db.go:92:func OpenReadOnly(path string) (*DB, error) {
internal/db/db.go:108:func (d *DB) migrate() error {
internal/db/db.go:201:func (d *DB) applyMigration(name, stmts string) error {
internal/ingest/ingest_test.go:27:func writeSession(t *testing.T, root, encodedDir, uuid string, lines ...string) string {
internal/ingest/ingest_test.go:44:func openTestDB(t *testing.T) *db.DB {
internal/ingest/ingest_test.go:92:func writeCodexRollout(t *testing.T, dir, uuid string) {
internal/ingest/ingest_test.go:550:func appendLine(t *testing.T, path, line string) {
internal/ingest/ingest_test.go:931:func rowCount(t *testing.T, d *db.DB, q string, args ...any) int {
internal/ingest/activity_test.go:281:func activityRows(t *testing.T, database *db.DB, sess string) []string {
internal/cli/mcp_lease_test.go:97:func testEnv(ing leaderIngester, renew time.Duration) *leaderEnv {
internal/cli/prune_test.go:23:func pruneSandbox(t *testing.T, uuid string, ageDays int) (*db.DB, string) {
internal/cli/prune_test.go:71:func rawJSONOf(t *testing.T, d *db.DB, uuid string) []string {
internal/cli/prune_test.go:132:func mustPrune(t *testing.T, d *db.DB, cutoff int64, uuid string) (int64, int64, error) {
internal/cli/prune_test.go:203:func reopen(t *testing.T) *db.DB {
internal/cli/prune_test.go:562:func runShowCmd(t *testing.T, args ...string) string {
internal/cli/index_purge_test.go:16:func runIndexCmd(t *testing.T, args ...string) (string, error) {
internal/doctor/doctor_test.go:200:func findResult(t *testing.T, results []Result, name string) Result {
internal/ingest/incremental.go:51:type FileState struct {
internal/ingest/ingest.go:34:type Ingester struct {
internal/ingest/ingest.go:88:type Stats struct {
internal/cli/mcp.go:120:type leaderIngester interface {
internal/sessions/prune.go:15:type PruneCandidate struct {
internal/sessions/prune.go:87:type PrunePair struct {
internal/sessions/prune.go:99:type PruneResult struct {
internal/db/db.go:29:type DB struct {
internal/registry/registry.go:28:const All = "all"
internal/db/migrations/0001_init.sql:16:    id           INTEGER PRIMARY KEY AUTOINCREMENT,
internal/db/activity.go:4:const ActivityWatermarkSQL = `SELECT COALESCE((SELECT checked_through FROM activity_backfill_state WHERE id=1),0)`
internal/db/activity.go:12:const PendingActivitySQL = `m.id > ? AND m.role = ?
internal/db/migrations/0012_prune_raw_support.sql:6:CREATE TRIGGER messages_au AFTER UPDATE OF content ON messages BEGIN
internal/ingest/incremental.go:10:const fingerprintWindow = 512
20:	evUser1   = `{"type":"user","timestamp":"2026-04-26T11:0
21:	evAsst1   = `{"type":"assistant","timestamp":"2026-04-26
22:	evResult1 = `{"type":"user","timestamp":"2026-04-26T11:0
23:	evUser2   = `{"type":"user","timestamp":"2026-04-26T11:0
internal/cli/common.go:115:	st, err := ing.IngestAll(ctx, projects, full)
internal/cli/install_mcp.go:48:				st, err := ing.IngestAll(cmd.Context(), projects, false)
internal/watcher/watcher.go:81:				if _, _, err := w.ing.IngestFile(ctx, path, false); err != nil {
internal/watcher/watcher.go:109:	if _, err := w.ing.IngestAll(ctx, w.projectsDir, false); err != nil && ctx.Err() == nil {
internal/cli/mcp.go:181:		_, err := env.ing.IngestAll(ctx, projects, false)
internal/cli/mcp.go:351:	if _, err := env.ing.IngestAll(wctx, projects, false); err != nil && wctx.Err() == nil {
cmd/clio-ingest-once/main.go:25:	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
0016_activity_backfill_watermark.sql
```

Baseline before any change: `go test ./internal/timeutil/ ./internal/db/ -count=1` → both `ok`; `go test ./internal/cli/ -run 'TestPrune|TestCheckPrunedResiduals|TestShowRawFormatPrunedNote' -count=1` → `ok`; `go test ./internal/ingest/ -run 'TestMigration0012|TestAbortedFlag' -count=1` → `ok`.

How the expected outputs were obtained: every Task was prototyped in a scratch copy of the repository (not in this working tree) and replayed three times in order from a copy of `7527cb8` (the last replay, after the 2026-11-07 decisions, follows this plan's step order exactly, including the Task 5 MCP steps and their negative control): each red step's output above is what the replay printed, each green step passed, `check_docs` printed nothing after Task 8, and after Task 8 `go test ./... -count=1` passed in every package and `gofmt -l internal` and `go vet ./...` printed nothing. Line numbers in the expected outputs assume each appended block is separated from the previous one by exactly one blank line, as the replay did. The diffs in Tasks 2–8 are `git diff` output from that replay; their `index` lines refer to the scratch repository and `git apply` ignores them.

---

## 已裁決（2026-11-07）

User decisions, relayed by the main conversation on 2026-11-07, on the items this plan raised:

1. **Write-time mechanism** — adopt "UPDATE before commit" as planned. Task 8 Step 5 measures `clio index --full` (retention off vs `14d`) and the sweep on a scratchpad copy of the real DB; report the numbers only, no threshold.
2. **When the setting is read** — as planned: once per `IngestAll` / `IngestFile` call; the bad-value warning logs once per Ingester.
3. **Aged sessions in MCP-only use** — the MCP leader's startup catch-up (`internal/cli/mcp.go:351`, `startupCatchup`) also runs the sweep, once per leader start; the per-call catch-up (`:181`) does not. A sweep failure is only logged and never stops MCP from starting. Implemented in Task 5 Steps 10–13 (`TestMCPStartupCatchUpSweeps`, `TestMCPStartupSweepFailureOnlyLogs`); the earlier `TestMCPCatchUpDoesNotSweep` became `TestReadCatchUpsDoNotSweep` (MCP per-call and CLI catch-ups); the openspec deltas (raw-json-retention, session-ingest, proposal, tasks) say the same. Sessions whose transcript is already deleted stay out of reach of every sweep, as with `prune-raw`.
4. **`clio config raw-retention` details** — as planned: `DisableFlagParsing`, reading without an index prints `off` and creates nothing, `30m` accepted.
5. **Age expression** — `COALESCE(ended_at, started_at, 0)`, as in the code. The design document is updated by the main conversation.
6. **`show --format json`** — as planned: no JSON-side retention hint.
7. **doctor footer** — as planned: unchanged; the `raw retention` line names its own fix.
8. **Sweep cost** — as planned; the Task 8 Step 5 measurement covers it (C − B, D − B).
9. **Error joining and summary wording** — as planned: `errors.Join` of residual and sweep failures; summary `raw retention 14d: pruned N messages across M sessions (X of raw_json)`.
10. **Codex coverage** — add a test: `TestRawRetentionBlanksOldCodexRollout` in Task 4, built with `writeCodexRollout` (`internal/ingest/ingest_test.go:92`).
11. **`cmd/clio-ingest-once`** — as planned: gets the write-time drop automatically, no sweep.
12. **CHANGELOG** — as planned: left to the release PR.

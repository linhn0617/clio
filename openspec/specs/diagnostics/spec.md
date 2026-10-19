# diagnostics Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.

## Requirements

### Requirement: Health diagnostics

The system SHALL provide `clio doctor` to check paths, DB integrity, ingest lag, orphaned
sessions, FTS health, file permissions on its private files, and the Claude Code
integration (hook binary, MCP registration, MCP lease, index freshness). The command SHALL
exit with a non-zero status when any check fails. A query or row-scan error while running a
check SHALL mark that check failed rather than be silently treated as a passing or
zero-count result. A check that was deliberately not run SHALL be printed as `[skip]`, SHALL
NOT count as a failure, and SHALL say why.

#### Scenario: Healthy system

- **WHEN** the user runs `clio doctor` on a healthy install
- **THEN** the system SHALL report all checks passing, including `PRAGMA integrity_check`,
  and exit with status 0

#### Scenario: Failed check exits non-zero

- **WHEN** any `clio doctor` check reports a warning/failure
- **THEN** the command SHALL print the failing check and exit with a non-zero status

#### Scenario: Query error marks the check failed

- **WHEN** a check's underlying query or row scan returns an error
- **THEN** the system SHALL mark that check as failed (surfacing the error detail) instead
  of reporting it as passing

#### Scenario: World-readable private file flagged

- **WHEN** the database file or its `-wal`/`-shm` sidecars (which hold indexed content)
  have a mode other than `0600`
- **THEN** `clio doctor` SHALL report a permissions warning naming the file and its mode

### Requirement: Source-of-truth reconciliation

The system SHALL detect semantic drift between the DB and the source `.jsonl` files,
including content that was read but could not be indexed. A file whose only unindexed bytes
are a last line without its newline SHALL NOT count as drift: ingest absorbs only complete
lines, so no re-index can clear it. Reconciliation SHALL report such files as an
informational count and pass.

#### Scenario: Truncated source file

- **WHEN** a `.jsonl` file is truncated or missing a tail relative to what was ingested
- **THEN** `clio doctor` SHALL flag the affected session as out of sync

#### Scenario: Unparseable source lines reported

- **WHEN** ingest has skipped one or more complete lines that could not be parsed
  (recorded in `ingest_state.unparsed_lines`)
- **THEN** `clio doctor` SHALL report the total count as a warning and suggest running
  `clio index --full` after upgrading clio; a total of zero SHALL NOT warn

#### Scenario: Partial last line is not drift

- **WHEN** a tracked file's only unindexed bytes are a last line without its newline
- **THEN** `source reconciliation` SHALL pass, reporting `1 ending in a partial line (indexed
  once complete)`

#### Scenario: Complete unindexed line is drift

- **WHEN** a tracked file holds a complete line beyond what was ingested
- **THEN** `source reconciliation` SHALL fail, counting the file under `new unindexed bytes`

### Requirement: Storage warning

The system SHALL warn when the database grows large relative to source data.

#### Scenario: Oversized index

- **WHEN** the DB size exceeds the expected multiple of source `.jsonl` size
- **THEN** `clio doctor` SHALL emit a size warning and suggest `--exclude-tool-output` rebuild

### Requirement: Diagnostics are source-partitioned

`clio doctor` SHALL report ingest health per source **by iterating the registered sources
from the source registry**, not by special-casing any source by name, distinguishing a
source that is healthy and reconciled from one whose rows are preserved because its root is
currently unavailable (purge skipped). A source whose root is optional and absent (for
example a tool that is not installed) SHALL be reported as not-installed, never as a
failure. It SHALL report any cross-source uuid conflicts recorded in `source_conflicts`,
and SHALL report files found under a registered source root that no adapter owns.

#### Scenario: A missing source root is reported as preserved, not healthy

- **WHEN** a source's root is unavailable and its rows are retained
- **THEN** `doctor` SHALL report that source's rows as preserved (root unavailable), not as fully reconciled

#### Scenario: Source conflicts are surfaced

- **WHEN** a `source_conflicts` row exists
- **THEN** `doctor` SHALL report the unindexed file and the conflicting sources

#### Scenario: Per-source health is driven by the registry

- **WHEN** `doctor` runs with `claude-code` and `codex` registered
- **THEN** it SHALL report each source's health by iterating the registry, producing the
  same per-source output as before without a codex-specific branch

### Requirement: doctor reports pruned raw_json
`clio doctor` SHALL report two well-defined figures (original raw_json lengths
are gone after pruning, so no per-raw byte estimate is fabricated): the exact
pruned-message count (`COUNT(*) WHERE raw_json=''`), and the whole-DB freelist
size (`freelist_count × page_size`) labeled as "reclaimable on VACUUM (whole-DB
freelist, not raw-only)". Informational, not a failing check.

#### Scenario: doctor surfaces pruned counts
- **WHEN** `clio doctor` runs on a database where some sessions have been pruned
- **THEN** the output includes the exact pruned-message count and the labeled
  whole-DB freelist figure

#### Scenario: doctor on an unpruned database
- **WHEN** `clio doctor` runs on a database with no pruned messages
- **THEN** the pruned-raw line reports zero (or is a clean pass), not an error

### Requirement: doctor reports retained sessions

`clio doctor` SHALL print a `retained sessions` line, always (including a count of 0), of the
form `N session(s) kept after their transcript was deleted (clio index --purge-missing to
drop)`, where N counts sessions whose `ingest_state.source_missing_at` is set. It is
informational, never a failing check. Source reconciliation SHALL count a vanished file that
is marked as retained rather than missing (missing is only a vanished, unmarked file under an
available root), and ingest coverage SHALL exclude marked rows from the tracked count, so
long retention neither warns nor masks a real coverage gap. On a database that predates the
`source_missing_at` column (doctor opens read-only, before migrations), nothing SHALL be
treated as retained and no check SHALL fail for the missing column.

#### Scenario: Retained session is reported and not a failure

- **WHEN** a session's transcript was deleted and its row is marked retained
- **THEN** `clio doctor` SHALL report `1 session(s) kept after their transcript was deleted
  (clio index --purge-missing to drop)` and source reconciliation SHALL pass

#### Scenario: Zero retained is still printed

- **WHEN** no session is retained
- **THEN** the `retained sessions` line SHALL report 0 as a passing check

#### Scenario: Unmarked vanished file is still missing

- **WHEN** a tracked file under an available root is gone but not yet marked
- **THEN** source reconciliation SHALL count it as missing and warn

#### Scenario: Coverage excludes retained rows

- **WHEN** a root has 2 files on disk, 1 tracked, and 1 retained row
- **THEN** ingest coverage SHALL report `2 on disk, 1 tracked` and warn

### Requirement: doctor reports FTS bloat

`clio doctor` SHALL print an `fts bloat` line of the form
`fts N MB vs messages M MB (ratio R)`, sizes taken from the `dbstat` virtual table for
`messages_fts_data` and `messages`. The check SHALL fail, appending
`run clio index --full to optimize`, when the ratio exceeds 2.0 and the FTS data is at least
16 MB (smaller databases are never flagged). If the measurement itself fails the line SHALL
read `unavailable (…)` and SHALL pass: an absent measuring tool is not an index defect.

#### Scenario: Bloated FTS index is flagged

- **WHEN** `messages_fts_data` is 2135 MB and `messages` is 557 MB
- **THEN** the line SHALL read `fts 2135.0 MB vs messages 557.0 MB (ratio 3.8); run clio
  index --full to optimize` and the check SHALL fail

#### Scenario: Healthy or small index passes

- **WHEN** the ratio is at most 2.0, or the FTS data is under 16 MB
- **THEN** the check SHALL pass

#### Scenario: Measurement failure is not red

- **WHEN** the `dbstat` query errors
- **THEN** the line SHALL say `unavailable` and the check SHALL pass

### Requirement: doctor --quick skips whole-database scans

`clio doctor --quick` SHALL NOT run `PRAGMA integrity_check`, `PRAGMA quick_check`, the FTS5
content integrity-check or the `dbstat` bloat measurement, on any connection. `db integrity` and
`fts bloat` SHALL appear as `[skip]` lines reading `skipped (--quick)`. `fts index` SHALL still
compare message and FTS row counts, and its detail SHALL end with `content check skipped
(--quick)`, also when that comparison fails or errors. Every other check SHALL still run.

#### Scenario: Quick run skips the heavy checks

- **WHEN** the user runs `clio doctor --quick` on a healthy index
- **THEN** `db integrity` and `fts bloat` SHALL print as `[skip] … skipped (--quick)`, `fts index`
  SHALL report the row counts with `content check skipped (--quick)`, and the exit status SHALL
  be 0

#### Scenario: Row counts fail in a quick run

- **WHEN** the FTS row-count comparison errors during `clio doctor --quick`
- **THEN** `fts index` SHALL fail and its detail SHALL still end with `content check skipped
  (--quick)`

#### Scenario: Full run is unchanged

- **WHEN** the user runs `clio doctor` without `--quick`
- **THEN** `integrity_check`, the FTS content check and the bloat measurement SHALL all run

### Requirement: Quick and full runs list the same checks

A `clio doctor --quick` run SHALL list the same check names, in the same order, as a full run, so
the two reports line up; only the verdicts of the skipped scans differ.

#### Scenario: Same rows in both modes

- **WHEN** the user runs `clio doctor` and then `clio doctor --quick` on the same index
- **THEN** both reports SHALL name the same checks in the same order

### Requirement: doctor checks the Claude Code integration

`clio doctor` (with or without `--quick`) SHALL run four read-only integration checks, `hook
binary`, `mcp registration`, `mcp lease` and `index freshness`, and SHALL NOT modify any file. A
check whose path cannot be resolved SHALL report that itself and SHALL NOT stop the others. A
failing integration check SHALL state its own remedy, and the index-rebuild hint SHALL be printed
only when a non-integration check failed.

#### Scenario: Unresolvable path

- **WHEN** the path to `~/.claude/settings.json` cannot be resolved
- **THEN** `hook binary` SHALL fail saying it cannot resolve the path, and the other three
  integration checks SHALL still run

#### Scenario: Integration failure only

- **WHEN** only integration checks fail
- **THEN** doctor SHALL exit non-zero without suggesting `clio index --full`

### Requirement: Hook binary check

`hook binary` SHALL judge clio's recall and file-history hook entries in
`~/.claude/settings.json` (binary `clio` or `clio.exe`) separately. Each entry's binary SHALL be
an absolute path to an existing executable (on Windows: a regular file), never resolved through
PATH or cwd. Every failure SHALL end with `; rerun clio install-hook`. With neither hook
installed the check SHALL pass, noting `not installed`.

#### Scenario: File-history under an unrelated matcher

- **WHEN** clio's file-history hook sits in a group whose matcher does not cover both Read and
  Bash (by install-hook's matcher rules) and is not the older `Read` shape, e.g. `Edit`
- **THEN** `hook binary` SHALL pass, noting `file-history hook matcher Edit does not cover
  Read|Bash`, since install-hook leaves the user's placement alone

#### Scenario: Both hooks healthy

- **WHEN** both hooks point at an existing executable
- **THEN** `hook binary` SHALL pass with `recall: <path>; file-history: <path>`

#### Scenario: Hook binary was removed by a reinstall

- **WHEN** `settings.json` holds a clio recall hook whose binary path no longer exists
- **THEN** `hook binary` SHALL fail with `recall hook points to missing binary <path>; rerun clio
  install-hook`

#### Scenario: Relative hook command

- **WHEN** a clio hook command starts with `clio`, `./clio`, `~/clio` or `$HOME/clio`
- **THEN** `hook binary` SHALL fail with `<kind> hook command is not an absolute path` without
  looking the name up

#### Scenario: Legacy unquoted hook path with whitespace

- **WHEN** a clio hook command is an unquoted absolute path containing a space, and the binary
  exists
- **THEN** `hook binary` SHALL fail with `<kind> hook has an unquoted path with whitespace; rerun
  clio install-hook`, because the shell splits the path and the hook cannot run

#### Scenario: Half install

- **WHEN** only one of the recall and file-history hooks is installed
- **THEN** `hook binary` SHALL fail with `<kind> hook not installed; rerun clio install-hook` for
  the missing one

#### Scenario: Older Read matcher

- **WHEN** clio's file-history hook sits in a PreToolUse group whose matcher is `Read` (the shape
  install-hook upgrades)
- **THEN** `hook binary` SHALL fail with `file-history hook matcher is Read, needs upgrade; rerun
  clio install-hook`

### Requirement: MCP registration check

`mcp registration` SHALL pass with `not registered (clio install-mcp)` when `mcpServers` has no
`clio` key; MCP is optional. An absolute `command` SHALL pass and be shown, or fail when it does
not exist. An entry without `command` SHALL pass as url-based. An entry that is not an object, or
whose `command` is not a non-empty string, SHALL fail with `malformed mcpServers.clio entry;
rerun clio install-mcp`.

#### Scenario: MCP not registered is not a warning

- **WHEN** `~/.claude.json` has no `clio` entry
- **THEN** `mcp registration` SHALL pass and doctor SHALL NOT fail because of it

#### Scenario: Bare MCP command

- **WHEN** the `clio` MCP entry's command is a bare name, such as install-mcp's `clio`
  fallback
- **THEN** `mcp registration` SHALL pass, noting that it is resolved on doctor's PATH, not Claude
  Code's, and that an absolute path is preferred, whether or not doctor's PATH finds it

#### Scenario: Relative MCP command with a separator

- **WHEN** the `clio` MCP entry's command is a relative path such as `bin/clio`
- **THEN** `mcp registration` SHALL pass without resolving it, noting it is resolved against
  Claude Code's working directory, not doctor's, and that an absolute path is preferred

#### Scenario: Registered binary is gone

- **WHEN** the `clio` MCP entry's absolute command no longer exists
- **THEN** `mcp registration` SHALL fail naming the path and suggesting `clio install-mcp`

### Requirement: MCP lease check

`mcp lease` SHALL pass with `no mcp running` when there is no lease file and with `mcp leader pid
N` for a live leader whose heartbeat is within the TTL. A live pid with an expired heartbeat, a
dead pid, and an unparsable lease file SHALL each fail with their own detail.

#### Scenario: Live leader with a late heartbeat

- **WHEN** the lease heartbeat is older than the TTL but the leader pid is running
- **THEN** `mcp lease` SHALL fail with `heartbeat Ns ago, pid N alive (leader may be catching up)`

#### Scenario: Dead leader

- **WHEN** the lease names a pid that is not running
- **THEN** `mcp lease` SHALL fail with `stale lease from pid N (dead); restart Claude Code or
  remove <lock path>`

### Requirement: Index freshness check

`index freshness` SHALL judge the numbers source reconciliation gathered, not inspect files again
or use the last ingest time. An unindexed change is a tracked file that was truncated, rewritten
at the same size, or holds a complete unindexed line; bytes without a newline (a partial last
line) are not one. It SHALL fail with `oldest unindexed change <age> old; run clio index` past 24
hours, and with `never indexed; run clio index` when `ingest_state` is empty.

#### Scenario: Quiet but complete index

- **WHEN** every tracked file is fully absorbed and the last ingest was days ago
- **THEN** `index freshness` SHALL pass with `no unindexed changes`

#### Scenario: Recent unindexed change

- **WHEN** the oldest unindexed change is in a file last modified 1 hour ago
- **THEN** `index freshness` SHALL pass with `oldest unindexed change 1h old (warns after 24h)`

#### Scenario: Index behind its sources

- **WHEN** a tracked file has an unindexed complete line and was last modified 30 hours ago
- **THEN** `index freshness` SHALL fail with `oldest unindexed change 30h old; run clio index`

#### Scenario: Session stopped mid-write

- **WHEN** a tracked file's only unindexed bytes are a last line without its newline, last
  modified 30 hours ago
- **THEN** `index freshness` and `source reconciliation` SHALL both pass, each noting the file
  that ends in a partial line, and doctor SHALL exit 0 when nothing else fails

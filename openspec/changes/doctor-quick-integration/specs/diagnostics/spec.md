## MODIFIED Requirements

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

## ADDED Requirements

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

- **WHEN** the `clio` MCP entry's command is not absolute, such as install-mcp's bare `clio`
  fallback
- **THEN** `mcp registration` SHALL pass, noting that it is resolved on doctor's PATH, not Claude
  Code's, and that an absolute path is preferred, whether or not doctor's PATH finds it

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
- **THEN** `index freshness` SHALL pass, noting the file ends in a partial line, and source
  reconciliation SHALL still count the file as having new unindexed bytes

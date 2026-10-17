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

## ADDED Requirements

### Requirement: doctor --quick skips whole-database scans

`clio doctor --quick` skips all integrity pragmas and FTS checks: it SHALL NOT run
`PRAGMA integrity_check` or `PRAGMA quick_check`, the FTS5 content integrity-check, or the
`dbstat` bloat measurement. Each skipped check (`db integrity`, `fts content check`,
`fts bloat`) SHALL appear as a `[skip]` line whose detail reads `skipped (--quick)`, including
when the cheap FTS row-count comparison fails or errors. The row-count comparison, source
reconciliation, ingest coverage, and every other check, including the integration checks, SHALL
still run. Plain `clio doctor` remains the deep check and behaves as before.

#### Scenario: Quick run skips the heavy checks

- **WHEN** the user runs `clio doctor --quick` on a healthy index
- **THEN** `db integrity`, `fts content check` and `fts bloat` SHALL print as
  `[skip] … skipped (--quick)` and the exit status SHALL be 0

#### Scenario: Full run is unchanged

- **WHEN** the user runs `clio doctor` without `--quick`
- **THEN** `integrity_check`, the FTS content check and the bloat measurement SHALL all run

### Requirement: doctor checks the Claude Code integration

`clio doctor` (with or without `--quick`) SHALL perform four read-only integration checks and
SHALL NOT modify any file. A check whose path cannot be resolved SHALL report that itself and
SHALL NOT stop the others. A failing integration check SHALL state its own remedy, and the
index-rebuild hint SHALL be printed only when a non-integration check failed.

- `hook binary`: for clio's recall and file-history hook entries in `~/.claude/settings.json`
  (binary named `clio`, or `clio.exe`), the binary path SHALL be absolute and name an existing
  file that is executable (on Windows: a regular file). A relative path (bare name, `./x`,
  `~/x`, `$HOME/x`) SHALL fail with `hook command is not an absolute path; rerun clio
  install-hook` and SHALL NOT be resolved through PATH or cwd. A missing binary SHALL fail with
  `hook points to missing binary <path>; rerun clio install-hook`. With no clio hook installed the
  check SHALL pass, noting `not installed`.
- `mcp registration`: with no `clio` key under `mcpServers` the check SHALL pass with `not
  registered (clio install-mcp)`; MCP is optional. An entry with a `command` SHALL pass and show
  it, or fail if that path does not exist; an entry without `command` (url-based) SHALL pass
  with `registered (url-based, no local binary to check)`; an entry that is not an object, or
  whose `command` is not a non-empty string, SHALL fail with `malformed mcpServers.clio entry;
  rerun clio install-mcp`.
- `mcp lease`: with no lease file the check SHALL pass (`no mcp running`); a live leader pid
  with a heartbeat within the TTL SHALL pass (`mcp leader pid N`); a live pid with an expired
  heartbeat SHALL fail with `heartbeat Ns ago, pid N alive (leader may be catching up)`; a dead
  pid SHALL fail with `stale lease from pid N (dead); restart Claude Code or remove <lock path>`;
  an unparsable lease file SHALL fail as unreadable.
- `index freshness`: the check SHALL measure whether the index keeps up with its sources, not
  when it last ingested. It SHALL fail with `index behind source by <age>; run clio index` only
  when a tracked file on disk holds bytes or a rewrite the index has not absorbed and that file
  was last modified more than 24 hours ago; otherwise it SHALL pass with `up to date (last
  change <age> ago)`. With no rows in `ingest_state` it SHALL fail with `never indexed`.

#### Scenario: Hook binary was removed by a reinstall

- **WHEN** `settings.json` holds a clio recall hook whose binary path no longer exists
- **THEN** `hook binary` SHALL fail naming that path and suggesting `clio install-hook`

#### Scenario: Relative hook command

- **WHEN** a clio hook command starts with `clio`, `./clio`, `~/clio` or `$HOME/clio`
- **THEN** `hook binary` SHALL fail as not an absolute path without looking the name up

#### Scenario: MCP not registered is not a warning

- **WHEN** `~/.claude.json` has no `clio` entry
- **THEN** `mcp registration` SHALL pass and doctor SHALL NOT fail because of it

#### Scenario: Live leader with a late heartbeat

- **WHEN** the lease heartbeat is older than the TTL but the leader pid is running
- **THEN** `mcp lease` SHALL fail saying the leader is alive and may be catching up

#### Scenario: Dead leader

- **WHEN** the lease names a pid that is not running
- **THEN** `mcp lease` SHALL fail saying the lease is stale and naming the lock path

#### Scenario: Quiet but complete index

- **WHEN** every tracked file is fully absorbed and the last ingest was days ago
- **THEN** `index freshness` SHALL pass

#### Scenario: Index behind its sources

- **WHEN** a tracked file has unabsorbed bytes and was last modified 30 hours ago
- **THEN** `index freshness` SHALL fail with `index behind source by 30h; run clio index`

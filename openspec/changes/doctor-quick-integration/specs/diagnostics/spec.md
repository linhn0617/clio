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

`clio doctor --quick` SHALL run `PRAGMA quick_check` in place of `PRAGMA integrity_check`
(the `db integrity` line SHALL say `quick_check`) and SHALL NOT run the FTS5 content
integrity-check nor the `dbstat` bloat measurement. Each skipped check (`integrity_check`,
`fts content check`, `fts bloat`) SHALL appear as a `[skip]` line whose detail reads
`skipped (--quick)`. The cheap FTS row-count comparison and every other check, including the
integration checks, SHALL still run. Without `--quick` behaviour is unchanged.

#### Scenario: Quick run skips the heavy checks

- **WHEN** the user runs `clio doctor --quick` on a healthy index
- **THEN** `db integrity` SHALL pass with `quick_check` in its detail, the three heavy
  checks SHALL print as `[skip] … skipped (--quick)`, and the exit status SHALL be 0

#### Scenario: Full run is unchanged

- **WHEN** the user runs `clio doctor` without `--quick`
- **THEN** `integrity_check`, the FTS content check and the bloat measurement SHALL all run

### Requirement: doctor checks the Claude Code integration

`clio doctor` (with or without `--quick`) SHALL perform four read-only integration checks and
SHALL NOT modify any file:

- `hook binary`: for clio's recall and file-history hook entries in `~/.claude/settings.json`,
  the first word of each command SHALL name an existing executable file. Otherwise the check
  SHALL fail with `hook points to missing binary <path>; rerun clio install-hook` (or the
  not-executable equivalent). With no clio hook installed the check SHALL pass, noting
  `not installed`.
- `mcp registration`: when `~/.claude.json` registers `clio` under `mcpServers`, the check
  SHALL pass and show the command, or fail if that path does not exist. When clio is not
  registered it SHALL pass with `not registered (clio install-mcp)`; MCP is optional.
- `mcp lease`: with no lease file the check SHALL pass (`no mcp running`); with a live
  leader whose heartbeat is within the TTL it SHALL pass (`mcp leader pid N`); with an
  expired heartbeat or a dead leader pid it SHALL fail (`stale lease (last heartbeat Ns
  ago)`).
- `index freshness`: the check SHALL fail with `index last updated <age> ago; run clio index`
  when `MAX(last_ingested_at)` over `ingest_state` is more than 24 hours old, and with
  `never indexed` when `ingest_state` has no rows.

#### Scenario: Hook binary was removed by a reinstall

- **WHEN** `settings.json` holds a clio recall hook whose binary path no longer exists
- **THEN** `hook binary` SHALL fail naming that path and suggesting `clio install-hook`

#### Scenario: MCP not registered is not a warning

- **WHEN** `~/.claude.json` has no `clio` entry
- **THEN** `mcp registration` SHALL pass and doctor SHALL NOT fail because of it

#### Scenario: Stale lease

- **WHEN** the lease file's heartbeat is older than the lease TTL
- **THEN** `mcp lease` SHALL fail reporting the heartbeat age in seconds

#### Scenario: Index not updated for a day

- **WHEN** the newest `last_ingested_at` is 48 hours old
- **THEN** `index freshness` SHALL fail and suggest `clio index`

## MODIFIED Requirements

### Requirement: pruned raw_json is restorable and restore failure is visible
Pruning SHALL be reversible for every eligible (restorable) session: a
subsequent `clio index --full` re-derives and re-stores `raw_json` from the
source file (Claude re-parse, Codex rollout). Because
full reindex logs per-file failures and continues, `clio index --full` SHALL,
after a full run, report and exit non-zero if any session still has pruned
`raw_json`, so an unrestored prune is never silent. The prune command SHALL
print the restore path on completion.

#### Scenario: Full reindex restores pruned raw_json for both sources
- **WHEN** Claude and Codex sessions are each pruned and then `clio
  index --full` runs with their source files present
- **THEN** each session's `raw_json` is repopulated

#### Scenario: Unrestored prune is surfaced
- **WHEN** after `clio index --full` some pruned session was not restored (e.g.
  its file became unreadable)
- **THEN** the command reports that session and exits non-zero

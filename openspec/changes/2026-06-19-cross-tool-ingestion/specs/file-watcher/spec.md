## ADDED Requirements

### Requirement: Ingest spans multiple source roots

In MCP mode the file watcher SHALL watch every registered source root and live-ingest new
activity from each. Deletion reconciliation SHALL be partitioned per root: a source root
that is missing or unavailable SHALL NOT cause its previously indexed rows to be purged,
and SHALL NOT be treated as authorization to purge another source's rows.

#### Scenario: A new Codex session is live-ingested

- **WHEN** the watcher is running and a new `~/.codex/sessions/**/rollout-*.jsonl` appears
- **THEN** it SHALL be ingested as a `codex` session without a manual command

#### Scenario: A missing Codex root does not purge its rows

- **WHEN** the Codex root is temporarily unavailable while the Claude Code root is healthy
- **THEN** indexed Codex rows SHALL be preserved, not purged

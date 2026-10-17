## MODIFIED Requirements

### Requirement: Aggregates are deterministic replacements
Each source's usage aggregate SHALL be a deterministic function of complete state — Claude:
recomputed by a dedicated usage pass over the session's **original source file** (not the
filtered messages table), deduplicated by outer message uuid with the later line in the file
winning; Codex: the latest observed cumulative `total_token_usage` of the session's single
rollout file; Gemini: recomputed during whole-file replay. Aggregate writes SHALL be limited to
replace, delete (when complete state yields no usage), and no-op (Codex tail without token
events); no delta accumulation is stored for aggregates (per-file diagnostic counters follow
their pass-scope semantics: whole-file passes replace, Codex incremental accumulates). A
session retained after its source file was deleted SHALL keep its aggregate.

#### Scenario: Repeated ingest is idempotent
- **WHEN** the same session is indexed twice (incremental tail or full)
- **THEN** its `session_usage` rows are identical after both runs

#### Scenario: Session deletion removes usage
- **WHEN** a session's rows are purged (`clio index --purge-missing` on a deleted source
  file), or a full-commit rewrite completes with a completed usage scan result
- **THEN** its previous `session_usage` rows are deleted (or replaced) in the same transaction

#### Scenario: Retained session keeps usage
- **WHEN** a session's source file is deleted and reconciliation only marks it retained
- **THEN** its `session_usage` rows SHALL be kept

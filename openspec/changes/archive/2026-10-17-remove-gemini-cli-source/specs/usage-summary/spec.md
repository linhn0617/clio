## MODIFIED Requirements

### Requirement: Aggregates are deterministic replacements
Each source's usage aggregate SHALL be a deterministic function of complete state — Claude:
recomputed by a dedicated usage pass over the session's **original source file** (not the
filtered messages table), deduplicated by outer message uuid with the later line in the file
winning; Codex: the latest observed cumulative `total_token_usage` of the session's single
rollout file. Aggregate writes SHALL be limited to
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

### Requirement: total_tokens is reproducibly defined per source
The `total_tokens` column SHALL store the source's native total when one exists (Codex
`total_token_usage.total_tokens`) and otherwise the fixed derived sum
input + output + cache_read + cache_creation (Claude). Categories present only in
`categories_json` SHALL NOT contribute to `total_tokens`. All ranking surfaces SHALL order by
this column.

#### Scenario: Claude derived total is stable
- **WHEN** a Claude session's usage is aggregated twice from the same file state
- **THEN** `total_tokens` equals input + output + cache_read + cache_creation both times

#### Scenario: Native totals win
- **WHEN** a Codex record carries a native total
- **THEN** `total_tokens` stores the native value, not a re-derived sum

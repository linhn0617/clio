## ADDED Requirements

### Requirement: Full-text search with trigram tokenizer

The system SHALL provide full-text search over indexed messages using an FTS5 trigram tokenizer.

#### Scenario: CJK substring match

- **WHEN** the user searches for `驗證`
- **THEN** the system SHALL return messages containing `資料驗證流程` and other substrings, because the trigram tokenizer matches sub-sequences

#### Scenario: Code identifier match

- **WHEN** the user searches for a camelCase or snake_case identifier
- **THEN** the system SHALL match occurrences inside code blocks and tool output

### Requirement: Relevance ranking favors dialogue and recency

The system SHALL rank results by bm25 adjusted with role weighting and a recency prior.

#### Scenario: Dialogue outranks log noise

- **WHEN** a query matches both a user/assistant discussion and a noisy log inside tool output
- **THEN** the system SHALL rank the user/assistant discussion higher

### Requirement: Result filters

The system SHALL support filtering by date range, project path, and role.

#### Scenario: Filtered search

- **WHEN** the user runs a search with `--since 7d --project <path> --role user`
- **THEN** the system SHALL restrict results to user messages from the last 7 days within that project

### Requirement: Tool output excluded by default

The system SHALL exclude tool output from search results unless explicitly requested.

#### Scenario: Default vs include-tool-output

- **WHEN** the user searches without `--include-tool-output`
- **THEN** the system SHALL return only user/assistant content; **WHEN** `--include-tool-output` is set, the system SHALL also include tool output in results

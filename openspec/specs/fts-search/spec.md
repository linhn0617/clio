# fts-search Specification

## Purpose
TBD - created by archiving change add-cli-and-mcp-foundation. Update Purpose after archive.
## Requirements
### Requirement: Full-text search with trigram tokenizer

The system SHALL provide full-text search over indexed messages using an FTS5
trigram tokenizer for query terms of 3+ runes. Queries whose terms are all shorter
than 3 runes use the substring fallback instead (see "Hybrid short/long term search").

#### Scenario: CJK trigram match (3+ runes)

- **WHEN** the user searches for a 3+ rune CJK query such as `資料驗`
- **THEN** the system SHALL return messages containing it as a sub-sequence (e.g.
  `資料驗證流程`), because the trigram tokenizer matches 3-gram sub-sequences

#### Scenario: Short CJK query uses the substring fallback

- **WHEN** the user searches for a 2-rune CJK query such as `驗證`
- **THEN** the system SHALL match it via the escaped substring scan — the trigram
  index needs 3+ runes — still returning messages containing `資料驗證流程`

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

### Requirement: Operator-safe full-text queries

The system SHALL NOT interpret user search input as FTS5 query operators and SHALL
NOT raise an FTS syntax error from user input. (Double quotes still group multi-word
phrases; each term is matched as a literal phrase.)

#### Scenario: Query with FTS-special characters

- **WHEN** a user searches for `c++`, `"unclosed`, `foo OR`, or `(test`
- **THEN** the system SHALL return results (or an empty set) without an FTS5 syntax
  error, matching the input as literal text

#### Scenario: Query with no searchable terms

- **WHEN** a user searches for input that is non-empty but parses to zero terms
  (only quote characters, e.g. `"`, `""`, or ` "" `)
- **THEN** the system SHALL return an empty result set without raising a SQL error

### Requirement: Hybrid short/long term search

The system SHALL use the FTS index for terms of 3+ runes and apply shorter terms as
filters over the FTS-narrowed rows, rather than degrading the whole query to a full
table scan when any term is short.

#### Scenario: Mixed-length multi-term query

- **WHEN** a user searches `auth ui` (one long term, one 2-rune term)
- **THEN** the system SHALL match messages containing both terms using the FTS index
  for `auth` and a filter for `ui`, without a full scan of `messages.content`

#### Scenario: All-short query

- **WHEN** every term is shorter than 3 runes
- **THEN** the system SHALL fall back to an escaped substring scan

### Requirement: LIKE wildcard escaping

The system SHALL escape `%`, `_`, and `\` in user-supplied values used in `LIKE`
patterns (content terms and project-path prefix) and use `ESCAPE '\'`.

#### Scenario: Project prefix containing an underscore

- **WHEN** a user filters by project prefix `/x/a_b`
- **THEN** the system SHALL match only paths beginning with the literal `/x/a_b`, not
  `/x/axb`


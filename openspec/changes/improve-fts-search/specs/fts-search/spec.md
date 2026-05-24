## ADDED Requirements

### Requirement: Literal-safe full-text queries

The system SHALL treat user search input as literal text, never as FTS5 query
operators, and SHALL NOT surface FTS syntax errors to the caller.

#### Scenario: Query with FTS-special characters

- **WHEN** a user searches for `c++`, `"unclosed`, `foo OR`, or `(test`
- **THEN** the system SHALL return results (or an empty set) without an FTS5 syntax
  error, matching the input as literal text

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
